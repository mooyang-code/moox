package command

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"path"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"gopkg.in/yaml.v2"
)

const defaultFactorEngineDeployDir = "/data/moox/factor-engine"

func isFactorEngineService(service string) bool {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "factor-engine", "moox-factor-engine", "moox_factor_engine":
		return true
	default:
		return false
	}
}

func requireDedicatedFactorEngineDeployDir(deployDir string) error {
	cleaned := path.Clean("/" + strings.Trim(strings.TrimSpace(deployDir), "/"))
	if cleaned == "/" || cleaned == "." {
		return fmt.Errorf("factor-engine requires --deploy-dir")
	}
	if cleaned == setupconfig.DefaultDeployRoot {
		return fmt.Errorf("factor-engine requires a dedicated --deploy-dir such as %s, not %s", defaultFactorEngineDeployDir, cleaned)
	}
	for _, root := range []string{setupconfig.DefaultControlRoot, setupconfig.DefaultStorageRoot} {
		if cleaned == root || strings.HasPrefix(cleaned, root+"/") {
			return fmt.Errorf("factor-engine requires a dedicated --deploy-dir such as %s, not %s", defaultFactorEngineDeployDir, cleaned)
		}
	}
	return nil
}

func validateFactorEngineEventBusCredential(raw []byte) error {
	var file struct {
		URLs []string `yaml:"urls"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("factor-engine EventBus credential is invalid")
	}
	if len(file.URLs) == 0 {
		return fmt.Errorf("factor-engine EventBus credential urls are missing")
	}
	for _, rawURL := range file.URLs {
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil || parsed.Scheme != "tls" || strings.TrimSpace(parsed.Host) == "" {
			return fmt.Errorf("factor-engine EventBus URL must be tls://")
		}
		host := parsed.Hostname()
		if strings.EqualFold(host, "localhost") {
			return fmt.Errorf("factor-engine EventBus URL must not be loopback")
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return fmt.Errorf("factor-engine EventBus URL must not be loopback")
		}
	}
	return nil
}

func factorEngineStorageRPC(snapshot *setupconfig.Snapshot) (target, nodeID string, err error) {
	if snapshot == nil {
		return "", "", fmt.Errorf("factor-engine storage gateway is missing")
	}
	for _, space := range snapshot.Manifest.SCFFetcher.Spaces {
		nodeID = strings.TrimSpace(space.StorageGatewayNodeID)
		host := strings.TrimSpace(space.StorageGatewayHost)
		if host == "" {
			continue
		}
		return "ip://" + net.JoinHostPort(host, "11003"), nodeID, nil
	}
	if snapshot.Manifest.HasStorageHost() {
		host := snapshot.Manifest.StorageHost
		nodeID = strings.TrimSpace(host.Name)
		return "ip://" + net.JoinHostPort(host.Address, "11003"), nodeID, nil
	}
	return "", "", fmt.Errorf("factor-engine storage gateway is missing")
}

func syncFactorEngineRuntimeFromControl(ctx context.Context, snapshot *setupconfig.Snapshot, target setupssh.Client, host setupconfig.Host) error {
	if snapshot == nil || target == nil {
		return fmt.Errorf("factor-engine runtime sync is invalid")
	}
	control := target
	if !strings.EqualFold(host.Name, snapshot.Manifest.ControlHost.Name) {
		remote, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
		if err != nil {
			return err
		}
		defer remote.Close()
		control = remote
	}
	return installFactorEngineRuntime(ctx, snapshot, target, control)
}

func installFactorEngineRuntime(ctx context.Context, snapshot *setupconfig.Snapshot, target, control setupssh.Client) error {
	if snapshot == nil || target == nil || control == nil {
		return fmt.Errorf("factor-engine runtime install is invalid")
	}
	gatewayTarget, gatewayNodeID, err := factorEngineStorageRPC(snapshot)
	if err != nil {
		return err
	}
	if strings.TrimSpace(gatewayNodeID) == "" {
		return fmt.Errorf("factor-engine storage gateway node id is missing")
	}
	controlRoot := snapshot.Manifest.Paths.Resolved().ControlRoot
	eventbusYAML, err := readRemoteControlFile(ctx, control, ".config/moox/eventbus/factor-engine-eventbus.yaml")
	if err != nil {
		return fmt.Errorf("read control factor-engine EventBus credential: %w", err)
	}
	if err := validateFactorEngineEventBusCredential(eventbusYAML); err != nil {
		return err
	}
	caPEM, err := readRemoteControlFile(ctx, control, ".config/moox/eventbus/ca.pem")
	if err != nil {
		return fmt.Errorf("read control EventBus CA for factor-engine: %w", err)
	}
	gatewayKey, err := readRemoteControlFile(ctx, control, path.Join(controlRoot, "secrets/gateway-factor.key"))
	if err != nil {
		return fmt.Errorf("read control gateway-factor key: %w", err)
	}
	storageAuth, err := readRemoteControlFile(ctx, control, path.Join(controlRoot, "secrets/storage-internal-auth.env"))
	if err != nil {
		return fmt.Errorf("read control storage-internal-auth: %w", err)
	}
	healthAuth, err := readRemoteControlFile(ctx, control, path.Join(controlRoot, "secrets/health-auth.env"))
	if err != nil {
		return fmt.Errorf("read control health-auth: %w", err)
	}
	homeResult, err := target.Run(ctx, []string{"sh", "-lc", `printf '%s' "$HOME"`}, nil)
	if err != nil || strings.TrimSpace(homeResult.Stdout) == "" {
		return fmt.Errorf("factor-engine home directory is unavailable")
	}
	home := strings.TrimSpace(homeResult.Stdout)
	if _, err := target.Run(ctx, []string{"sh", "-lc", `mkdir -p "$HOME/.config/moox/eventbus" "$HOME/.config/moox/factor-engine" && chmod 700 "$HOME/.config/moox" "$HOME/.config/moox/eventbus" "$HOME/.config/moox/factor-engine"`}, nil); err != nil {
		return fmt.Errorf("factor-engine config directories are unavailable")
	}
	runtimeEnv := fmt.Sprintf("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=%s\nMOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID=%s\n", gatewayTarget, gatewayNodeID)
	uploads := []struct {
		rel  string
		data []byte
	}{
		{path.Join(".config/moox/eventbus/factor-engine-eventbus.yaml"), eventbusYAML},
		{path.Join(".config/moox/eventbus/ca.pem"), caPEM},
		{path.Join(".config/moox/factor-engine/gateway-factor.key"), gatewayKey},
		{path.Join(".config/moox/factor-engine/storage-internal-auth.env"), storageAuth},
		{path.Join(".config/moox/factor-engine/health-auth.env"), healthAuth},
		{path.Join(".config/moox/factor-engine/runtime.env"), []byte(runtimeEnv)},
	}
	for _, item := range uploads {
		dst := path.Join(home, item.rel)
		if err := target.Upload(ctx, bytes.NewReader(item.data), int64(len(item.data)), dst, fs.FileMode(0o600)); err != nil {
			return fmt.Errorf("upload factor-engine runtime material failed: %s: %w", dst, err)
		}
	}
	return nil
}
