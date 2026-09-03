package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const skillConfigIdentity = "moox-skill"

type skillSecretReader func(context.Context, setupconfig.Host, string) ([]byte, error)

type skillGatewaySnapshotReader func(context.Context, setupconfig.Host, string) (skillGatewaySnapshot, error)

type skillGatewaySnapshot struct {
	Secret   []byte
	NodeID   string
	Registry []byte
}

type skillGatewayCredentialRegistry struct {
	Version     int                                   `json:"version"`
	Credentials []skillGatewayCredentialRegistryEntry `json:"credentials"`
}

type skillGatewayCredentialRegistryEntry struct {
	KeyID      string `json:"key_id"`
	Caller     string `json:"caller"`
	SecretFile string `json:"secret_file"`
}

func newSetupExportSkillConfigCommand(deps setupDeps) *cobra.Command {
	var file, space, output string
	cmd := &cobra.Command{
		Use:   "export-skill-config",
		Short: "导出 Skill 使用的最小数据访问配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			space = strings.TrimSpace(space)
			output = strings.TrimSpace(output)
			if space == "" {
				return fmt.Errorf("--space is required")
			}
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := rejectInputOutputCollision(file, output); err != nil {
				return fmt.Errorf("write skill config: %w", err)
			}

			config, err := deps.exportSkillConfig(cmd.Context(), snapshot, space)
			if err != nil {
				return err
			}
			if err := config.validate(); err != nil {
				return fmt.Errorf("skill_config_invalid: %w", err)
			}
			raw, err := yaml.Marshal(config)
			if err != nil {
				return fmt.Errorf("encode skill config: %w", err)
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			if err := writeSkillConfigAtomic0600(output, raw, os.Rename); err != nil {
				return fmt.Errorf("write skill config: %w", err)
			}
			return writeSetupJSON(cmd, map[string]string{"status": "exported", "output": output})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&space, "space", "", "要导出的 SCF Space ID")
	cmd.Flags().StringVar(&output, "output", "", "Skill 数据访问配置输出路径")
	_ = cmd.MarkFlagRequired("space")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func defaultSetupExportSkillConfig(ctx context.Context, snapshot *setupconfig.Snapshot, space string) (dataAccessConfig, error) {
	if snapshot == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: setup snapshot is required")
	}
	transports := make(map[string]setupssh.Client)
	defer func() {
		for _, transport := range transports {
			_ = transport.Close()
		}
	}()
	transportFor := func(ctx context.Context, host setupconfig.Host) (setupssh.Client, error) {
		key := host.Name + "\x00" + host.Address
		transport := transports[key]
		if transport == nil {
			var err error
			transport, err = dialSetupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("connect deployment host: %w", err)
			}
			transports[key] = transport
		}
		return transport, nil
	}
	read := func(ctx context.Context, host setupconfig.Host, path string) ([]byte, error) {
		transport, err := transportFor(ctx, host)
		if err != nil {
			return nil, err
		}
		return readRemoteSkillSecret(ctx, transport, path)
	}
	readGateway := func(ctx context.Context, host setupconfig.Host, root string) (skillGatewaySnapshot, error) {
		transport, err := transportFor(ctx, host)
		if err != nil {
			return skillGatewaySnapshot{}, err
		}
		return readRemoteSkillGatewaySnapshot(ctx, transport, root)
	}
	return buildSkillDataAccessConfig(ctx, snapshot, space, readGateway, read)
}

func buildSkillDataAccessConfig(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaceID string,
	readGateway skillGatewaySnapshotReader,
	read skillSecretReader,
) (dataAccessConfig, error) {
	if snapshot == nil || readGateway == nil || read == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: dependencies are required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if strings.EqualFold(spaceID, "crypto_market") {
		spaceID = "crypto"
	}
	if spaceID != "crypto" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: unsupported space %q", spaceID)
	}
	var selected *setupconfig.SCFFetcherSpace
	for index := range snapshot.Manifest.SCFFetcher.Spaces {
		candidate := &snapshot.Manifest.SCFFetcher.Spaces[index]
		candidateSpaceID := strings.TrimSpace(candidate.SpaceID)
		if strings.EqualFold(candidateSpaceID, "crypto_market") {
			candidateSpaceID = "crypto"
		}
		if candidateSpaceID == spaceID {
			selected = candidate
			break
		}
	}
	if selected == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: scf_fetcher has no configuration for space %q", spaceID)
	}
	target := strings.TrimSpace(selected.StorageRPCGatewayTarget)
	if target == "" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: space %q storage_rpc_gateway_target is required", spaceID)
	}
	targetNode := strings.TrimSpace(selected.StorageGatewayNodeID)
	if targetNode == "" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: space %q storage_gateway_node_id is required", spaceID)
	}
	gatewayHost, gatewayRoot, canonicalNodeID, err := resolveSkillGatewayPlacement(snapshot.Manifest, targetNode)
	if err != nil {
		return dataAccessConfig{}, err
	}
	if err := validateSkillGatewayTarget(target, gatewayHost); err != nil {
		return dataAccessConfig{}, err
	}
	storageRoot := strings.TrimSpace(snapshot.Manifest.Paths.Resolved().StorageRoot)
	if storageRoot == "" || !filepath.IsAbs(storageRoot) {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Storage deployment placement unavailable")
	}

	gatewaySnapshot, err := readGateway(ctx, gatewayHost, gatewayRoot)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Gateway snapshot unavailable: %w", err)
	}
	gatewaySecret, err := normalizeSkillGatewaySecret(gatewaySnapshot.Secret)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Gateway credential invalid")
	}
	deployedNodeID := strings.TrimSpace(gatewaySnapshot.NodeID)
	if deployedNodeID == "" || deployedNodeID != canonicalNodeID {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Gateway identity does not match configured target")
	}
	if err := validateSkillGatewayCredentialRegistry(gatewaySnapshot.Registry, filepath.Join(gatewayRoot, "secrets/gateway-moox-skill.key")); err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Gateway credential registry invalid: %w", err)
	}
	gatewaySnapshot = skillGatewaySnapshot{}

	storageRaw, err := read(ctx, gatewayHost, filepath.Join(storageRoot, "secrets/storage-internal-auth.env"))
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Storage auth unavailable")
	}
	primarySecret, err := collectorStoragePrimaryAuthSecret(storageRaw)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Storage auth invalid")
	}
	storageAppKey := security.HMACSHA256Hex(primarySecret, []byte(skillConfigIdentity))
	primarySecret = ""
	storageRaw = nil

	return dataAccessConfig{
		Version: 1,
		Gateway: dataGatewayConfig{
			Target: target, TargetNode: canonicalNodeID, KeyID: skillConfigIdentity,
			Caller: skillConfigIdentity, Secret: gatewaySecret,
		},
		Storage: dataStorageAuthConfig{AppID: skillConfigIdentity, AppKey: storageAppKey},
		DataTypes: map[string]dataTypeConfig{
			"crypto": {
				DefaultExchange: "binance",
				Exchanges: map[string]exchangeConfig{
					"binance": {
						SpaceID: "crypto", SeriesTag: "venue:binance",
						KlineDatasets: map[string]string{"1m": "binance_spot_kline_1m"},
					},
				},
			},
			"stock_cn": {
				DefaultExchange: "stock_cn",
				Exchanges: map[string]exchangeConfig{
					"stock_cn": {
						SpaceID: "stock_cn", SeriesTag: "",
						KlineDatasets: map[string]string{"1m": "stock_cn_kline"},
					},
				},
			},
		},
	}, nil
}

func validateSkillGatewayCredentialRegistry(raw []byte, expectedSecretPath string) error {
	if len(raw) == 0 || len(raw) > 4096 {
		return fmt.Errorf("invalid size")
	}
	var registry skillGatewayCredentialRegistry
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return fmt.Errorf("decode registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return fmt.Errorf("decode trailing registry content: %w", err)
	}
	if registry.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	if len(registry.Credentials) == 0 {
		return fmt.Errorf("credentials are required")
	}
	keyIDs := make(map[string]struct{}, len(registry.Credentials))
	callers := make(map[string]struct{}, len(registry.Credentials))
	skillEntries := 0
	for _, entry := range registry.Credentials {
		if !validSkillRegistryIdentifier(entry.KeyID) || !validSkillRegistryIdentifier(entry.Caller) || strings.TrimSpace(entry.SecretFile) == "" {
			return fmt.Errorf("credential entry is invalid")
		}
		if _, exists := keyIDs[entry.KeyID]; exists {
			return fmt.Errorf("duplicate key_id %q", entry.KeyID)
		}
		keyIDs[entry.KeyID] = struct{}{}
		if _, exists := callers[entry.Caller]; exists {
			return fmt.Errorf("duplicate caller %q", entry.Caller)
		}
		callers[entry.Caller] = struct{}{}

		if entry.KeyID != skillConfigIdentity && entry.Caller != skillConfigIdentity {
			continue
		}
		skillEntries++
		if entry.KeyID != skillConfigIdentity || entry.Caller != skillConfigIdentity {
			return fmt.Errorf("moox-skill key_id and caller must match")
		}
		if entry.SecretFile != filepath.Base(expectedSecretPath) {
			return fmt.Errorf("moox-skill secret_file must be %q", filepath.Base(expectedSecretPath))
		}
		resolved := filepath.Join(filepath.Dir(expectedSecretPath), entry.SecretFile)
		if filepath.Clean(resolved) != filepath.Clean(expectedSecretPath) {
			return fmt.Errorf("moox-skill secret_file path does not match deployed key")
		}
	}
	if skillEntries != 1 {
		return fmt.Errorf("moox-skill must be registered exactly once")
	}
	return nil
}

func validSkillRegistryIdentifier(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func resolveSkillGatewayPlacement(manifest setupconfig.Manifest, configuredNodeID string) (setupconfig.Host, string, string, error) {
	configuredNodeID = strings.TrimSpace(configuredNodeID)
	paths := manifest.Paths.Resolved()
	if strings.EqualFold(configuredNodeID, "control") || strings.EqualFold(configuredNodeID, manifest.ControlHost.Name) {
		return manifest.ControlHost, paths.ControlRoot, "control", nil
	}
	host, err := findSetupHost(manifest, configuredNodeID)
	if err != nil {
		return setupconfig.Host{}, "", "", fmt.Errorf("skill_config: storage gateway node %q is not a configured host", configuredNodeID)
	}
	return host, paths.StorageRoot, host.Name, nil
}

func validateSkillGatewayTarget(target string, host setupconfig.Host) error {
	targetHost, err := validateNativeGatewayTarget(target)
	if err != nil {
		return fmt.Errorf("skill_config: storage_rpc_gateway_target %w", err)
	}
	selectedHost := strings.TrimSpace(host.Address)
	targetIP := net.ParseIP(targetHost)
	selectedIP := net.ParseIP(selectedHost)
	if targetIP != nil || selectedIP != nil {
		if targetIP == nil || selectedIP == nil || !targetIP.Equal(selectedIP) {
			return fmt.Errorf("skill_config: storage_rpc_gateway_target host does not match selected Gateway host")
		}
		return nil
	}
	normalizeHostname := func(value string) string {
		return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	}
	if normalizeHostname(targetHost) == "" || normalizeHostname(targetHost) != normalizeHostname(selectedHost) {
		return fmt.Errorf("skill_config: storage_rpc_gateway_target host does not match selected Gateway host")
	}
	return nil
}

func validateNativeGatewayTarget(target string) (string, error) {
	targetHost, port, err := parseGatewayTarget(target)
	if err != nil {
		return "", fmt.Errorf("must be ip://host:11003")
	}
	if port != 11003 {
		return "", fmt.Errorf("must use Native Gateway port 11003")
	}
	return targetHost, nil
}

func validateDataGatewayTarget(target string) (string, error) {
	targetHost, _, err := parseGatewayTarget(target)
	if err != nil {
		return "", fmt.Errorf("must be ip://host:port")
	}
	return targetHost, nil
}

func parseGatewayTarget(target string) (string, int, error) {
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil || parsed.Scheme != "ip" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", 0, fmt.Errorf("invalid gateway target")
	}
	targetHost, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil || strings.TrimSpace(targetHost) == "" {
		return "", 0, fmt.Errorf("invalid gateway target")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid gateway port")
	}
	return targetHost, port, nil
}

func normalizeSkillGatewaySecret(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return "", fmt.Errorf("invalid credential")
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" || strings.ContainsAny(secret, "\r\n") || !storageSecretValuePattern.MatchString(secret) {
		return "", fmt.Errorf("invalid credential")
	}
	return secret, nil
}

func readRemoteSkillSecret(ctx context.Context, transport setupssh.Client, path string) ([]byte, error) {
	if transport == nil || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	result, err := transport.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
path="$1"
file_id() { stat -c '%d:%i:%s:%Y' "$1" 2>/dev/null || stat -f '%d:%i:%z:%m' "$1"; }
file_hash() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
attempt=1
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT HUP INT TERM
while [ "$attempt" -le 3 ]; do
  [ -f "$path" ] && [ ! -L "$path" ] || exit 1
  mode=$(stat -c '%a' "$path" 2>/dev/null || stat -f '%Lp' "$path")
  [ "$mode" = 600 ] || exit 1
  size=$(wc -c <"$path")
  [ "$size" -gt 0 ] && [ "$size" -le 4096 ] || exit 1
  before="$(file_id "$path"):$(file_hash "$path")"
  cp "$path" "$tmp"
  after="$(file_id "$path"):$(file_hash "$path")"
  if [ "$before" = "$after" ]; then cat "$tmp"; exit 0; fi
  attempt=$((attempt + 1))
done
exit 1`,
		"moox-read-skill-secret", path,
	}, nil)
	if err != nil || len(result.Stdout) == 0 || len(result.Stdout) > 4096 {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	return []byte(result.Stdout), nil
}

func readRemoteSkillGatewaySnapshot(ctx context.Context, transport setupssh.Client, root string) (skillGatewaySnapshot, error) {
	if transport == nil || !filepath.IsAbs(root) {
		return skillGatewaySnapshot{}, fmt.Errorf("Gateway snapshot unavailable")
	}
	result, err := transport.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
root="$1"
secrets="$root/secrets"
key="$secrets/gateway-moox-skill.key"
env="$secrets/gateway-service.env"
registry="$secrets/gateway-credentials.json"
file_id() { stat -c '%d:%i:%s:%Y' "$1" 2>/dev/null || stat -f '%d:%i:%z:%m' "$1"; }
file_hash() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
validate_file() {
  [ -f "$1" ] && [ ! -L "$1" ] || return 1
  mode=$(stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1")
  [ "$mode" = 600 ] || return 1
  size=$(wc -c <"$1")
  [ "$size" -gt 0 ] && [ "$size" -le 4096 ]
}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
attempt=1
while [ "$attempt" -le 3 ]; do
  validate_file "$key" && validate_file "$env" && validate_file "$registry" || exit 1
  before="$(file_id "$key"):$(file_hash "$key")|$(file_id "$env"):$(file_hash "$env")|$(file_id "$registry"):$(file_hash "$registry")"
  cp "$key" "$tmp/key"
  cp "$env" "$tmp/env"
  cp "$registry" "$tmp/registry"
  after="$(file_id "$key"):$(file_hash "$key")|$(file_id "$env"):$(file_hash "$env")|$(file_id "$registry"):$(file_hash "$registry")"
  if [ "$before" = "$after" ]; then
    count=$(awk -F= '$1 == "MOOX_GATEWAY_NODE_ID" { count++ } END { print count + 0 }' "$tmp/env")
    [ "$count" -eq 1 ] || exit 1
    node=$(sed -n 's/^MOOX_GATEWAY_NODE_ID=//p' "$tmp/env")
    case "$node" in ''|*[!A-Za-z0-9._-]*) exit 1 ;; esac
    printf 'MOOX_SKILL_GATEWAY_SNAPSHOT_V1\n'
    base64 <"$tmp/key" | tr -d '\n'; printf '\n%s\n' "$node"
    base64 <"$tmp/registry" | tr -d '\n'; printf '\n'
    exit 0
  fi
  attempt=$((attempt + 1))
done
exit 1`,
		"moox-read-skill-gateway-snapshot", root,
	}, nil)
	if err != nil || len(result.Stdout) == 0 || len(result.Stdout) > 16384 {
		return skillGatewaySnapshot{}, fmt.Errorf("Gateway snapshot unavailable")
	}
	lines := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
	if len(lines) != 4 || lines[0] != "MOOX_SKILL_GATEWAY_SNAPSHOT_V1" || lines[2] == "" || len(lines[2]) > 256 || strings.ContainsAny(lines[2], "\r") {
		return skillGatewaySnapshot{}, fmt.Errorf("Gateway snapshot unavailable")
	}
	secret, err := base64.StdEncoding.Strict().DecodeString(lines[1])
	if err != nil || len(secret) == 0 || len(secret) > 4096 {
		return skillGatewaySnapshot{}, fmt.Errorf("Gateway snapshot unavailable")
	}
	registry, err := base64.StdEncoding.Strict().DecodeString(lines[3])
	if err != nil || len(registry) == 0 || len(registry) > 4096 {
		return skillGatewaySnapshot{}, fmt.Errorf("Gateway snapshot unavailable")
	}
	return skillGatewaySnapshot{Secret: secret, NodeID: lines[2], Registry: registry}, nil
}

func readRemoteGatewayNodeID(ctx context.Context, transport setupssh.Client, path string) (string, error) {
	if transport == nil || !filepath.IsAbs(path) {
		return "", fmt.Errorf("Gateway node ID unavailable")
	}
	result, err := transport.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
path="$1"
[ -f "$path" ]
[ ! -L "$path" ]
mode=$(stat -c '%a' "$path" 2>/dev/null || stat -f '%Lp' "$path")
[ "$mode" = 600 ]
size=$(wc -c <"$path")
[ "$size" -gt 0 ]
[ "$size" -le 4096 ]
count=$(awk -F= '$1 == "MOOX_GATEWAY_NODE_ID" { count++ } END { print count + 0 }' "$path")
[ "$count" -eq 1 ]
value=$(sed -n 's/^MOOX_GATEWAY_NODE_ID=//p' "$path")
case "$value" in
  ''|*[!A-Za-z0-9._-]*) exit 1 ;;
esac
printf '%s' "$value"`,
		"moox-read-gateway-target-node", path,
	}, nil)
	if err != nil || result.Stdout == "" || len(result.Stdout) > 256 || strings.ContainsAny(result.Stdout, "\r\n") {
		return "", fmt.Errorf("Gateway node ID unavailable")
	}
	return result.Stdout, nil
}

func writeSkillConfigAtomic0600(path string, content []byte, rename func(string, string) error) (err error) {
	if rename == nil {
		return fmt.Errorf("rename dependency is required")
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output %q must not be a symlink", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output %q must be a regular file", path)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temp.Write(content); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = rename(tempPath, path); err != nil {
		return err
	}
	return nil
}
