package unitdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbootstrap"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"gopkg.in/yaml.v3"
)

type ControlOptions struct {
	CoreOptions
	ProxyCA unitbootstrap.ProxyCAAuthorization
	// Release 非空时升级 control：使用该版本的 host 与 control 软件，不顺带部署 Storage 与出口单元。
	Release string
}

// BootstrapControl promotes the original core software to every control
// placement. It does not rerun the core-only request or replace its receipt.
func BootstrapControl(ctx context.Context, snapshot *setupconfig.Snapshot, options ControlOptions) (CoreResult, error) {
	return bootstrap(ctx, snapshot, options.CoreOptions, &options)
}

func controlInput(ctx context.Context, snapshot *setupconfig.Snapshot, root *os.Root, transport setupssh.Client, request unitbootstrap.Request, identity runtimeIdentity, software unitpackage.Result, options ControlOptions) (unitbootstrap.Request, map[string][]byte, error) {
	components, err := unitComponents(snapshot.Manifest, request.Topology.ControlHostID, "control")
	if err != nil {
		return request, nil, err
	}
	request.Control.Components = components
	request.Control.Environment, err = unitEnvironment(snapshot.Manifest, identity, request.Topology.ControlHostID, components)
	if err != nil {
		return request, nil, err
	}
	request.Control.Environment["admin"]["MOOX_ADMIN_JWT_SECRET_KEY"] = identity.JWTSecret
	request.ProxyCA = options.ProxyCA
	if request.ProxyCA.Create && consoleTLSMode(snapshot.Manifest.ControlHost()) == "public" {
		return request, nil, errors.New("proxy CA creation requires internal TLS and cannot be combined with trusted import")
	}
	if options.Release != "" {
		if err := checkControlDependencyRecords(snapshot, root, components); err != nil {
			return request, nil, err
		}
	} else if err := checkControlDependencies(ctx, snapshot, root, components, options.CoreOptions); err != nil {
		return request, nil, err
	}
	temporary, err := os.MkdirTemp(root.Name(), "control-templates-")
	if err != nil {
		return request, nil, err
	}
	defer os.RemoveAll(temporary)
	extraction, err := unitpackage.Extract(ctx, unitpackage.ExtractOptions{Archive: software.Archive, ExpectedSHA256: software.SHA256, Profile: "control", GOOS: "linux", GOARCH: software.Manifest.GOARCH, Destination: filepath.Join(temporary, "software")})
	if err != nil {
		return request, nil, err
	}
	overrides, err := controlConfigurations(snapshot, extraction.Directory, components)
	return request, overrides, err
}

func consoleTLSMode(host setupconfig.Host) string {
	if host.TLSMode == "internal" || host.TLSMode == "public" {
		return host.TLSMode
	}
	ip := net.ParseIP(host.Address)
	if strings.EqualFold(host.Address, "localhost") || strings.HasSuffix(strings.ToLower(host.Address), ".localhost") || ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return "internal"
	}
	return "public"
}

// Templates come from the independently verified original package, never
// from files that may have changed in the worktree after the core build.
func controlConfigurations(snapshot *setupconfig.Snapshot, directory string, components []string) (map[string][]byte, error) {
	result := map[string][]byte{}
	control := snapshot.Manifest.ControlHost()
	mode := consoleTLSMode(control)
	for _, id := range components {
		if !slices.Contains([]string{"console-proxy", "monitor", "collector"}, id) {
			continue
		}
		name := id + "/config/app.yaml"
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
			return nil, errors.New("original control software lacks a bounded configuration template")
		}
		if id == "collector" {
			raw, err = setupconfig.RenderCollectorRuntimeConfig(snapshot, raw)
			if err != nil {
				return nil, err
			}
		}
		var configuration map[string]any
		if yaml.Unmarshal(raw, &configuration) != nil || configuration == nil {
			return nil, errors.New("original control template is not a YAML mapping")
		}
		mapping := func(key string) (map[string]any, error) {
			value, ok := configuration[key].(map[string]any)
			if !ok {
				return nil, errors.New("original control configuration lacks a required mapping")
			}
			return value, nil
		}
		switch id {
		case "console-proxy":
			public, err := mapping("public")
			if err != nil {
				return nil, err
			}
			public["host"] = control.Address
			tls, err := mapping("tls")
			if err != nil {
				return nil, err
			}
			tls["mode"] = mode
		case "monitor":
			placement, err := mapping("placement")
			if err != nil {
				return nil, err
			}
			// 控制主机上的组件由同机的 Monitor 探测，直接连回环地址（公网地址从本机常常访问不通）。
			placement["local_host_id"] = control.Name
			if slices.Contains(components, "console-proxy") {
				endpoint := (&url.URL{Scheme: "https", Host: net.JoinHostPort(control.Address, "9527"), Path: "/"}).String()
				target := map[string]string{"url": endpoint, "connect_address": net.JoinHostPort(control.Address, "9527"), "server_name": control.Address, "trust_mode": mode}
				if mode == "internal" {
					target["ca_file"], target["ca_baseline"] = "../../console-proxy/certs/caddy/root.crt", "../../console-proxy/data/caddy/internal-ca.sha256"
				}
				placement["https"] = map[string]any{"console-proxy": target}
			}
			metrics, err := mapping("metrics")
			if err != nil {
				return nil, err
			}
			metrics["dataset_health_policy_path"] = "config/dataset-health-policy.yaml"
		case "collector":
			storage, err := mapping("storage")
			if err != nil {
				return nil, err
			}
			node := snapshot.Manifest.PlacementHost("storage-node")
			if node == "" {
				return nil, errors.New("Collector requires a registered Storage node placement")
			}
			// 与 unit.go 里 Storage 节点的固定 ID 一致（元数据种子也使用该 ID）。
			storage["result_data_node_id"] = storageNodeID
		}
		result[name], err = yaml.Marshal(configuration)
		if err != nil {
			return nil, errors.New("cannot encode normalized control configuration")
		}
	}
	return result, nil
}

func persistPrivateBytes(root *os.Root, name string, raw []byte) error {
	if _, err := root.Lstat(name); err == nil {
		stored, err := fsutil.ReadPrivate(root, name, 4<<20)
		if err != nil || !bytes.Equal(stored, raw) {
			return errors.New("original private deployment input changed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return fsutil.WritePrivate(root, name, raw, false)
}

// bindControlRequest 把 control 请求摘要绑定到操作记录：首次完整 control 用 control-operation.json，
// 每个升级版本各自一条 control-operations/<版本>.json；重试同一版本必须是同一请求。
func bindControlRequest(root *os.Root, release, digest string) error {
	raw, err := json.Marshal(struct {
		Version int    `json:"version"`
		SHA256  string `json:"sha256"`
	}{1, digest})
	if err != nil {
		return err
	}
	name := "control-operation.json"
	if release != "" {
		if !validReleaseID(release) {
			return errors.New("invalid release ID")
		}
		name = "control-operations/" + release + ".json"
	}
	return persistPrivateBytes(root, name, append(raw, '\n'))
}

// checkControlDependencyRecords 是 control 升级时的依赖检查：只核验 Storage 与出口单元已有部署记录，
// 不重新部署它们（升级它们是各自单元的事）。
func checkControlDependencyRecords(snapshot *setupconfig.Snapshot, root *os.Root, components []string) error {
	profiles := []string{}
	if slices.ContainsFunc(components, func(id string) bool {
		return slices.Contains([]string{"monitor", "collector", "factor-mgr", "archive-mgr", "strategy"}, id)
	}) {
		profiles = append(profiles, "storage")
	}
	if slices.Contains(components, "collector") {
		profiles = append(profiles, "egress-proxy")
	}
	for _, profile := range profiles {
		found := false
		for _, host := range snapshot.Manifest.Hosts() {
			if _, err := unitComponents(snapshot.Manifest, host.Name, profile); err != nil {
				continue
			}
			if _, err := root.Lstat(filepath.Join("units", host.Name, profile, "operation.json")); err != nil {
				return fmt.Errorf("control upgrade requires an existing %s deployment on %s", profile, host.Name)
			}
			found = true
		}
		if !found {
			return errors.New("control upgrade is missing a required Storage or egress placement")
		}
	}
	return nil
}

func activeControlStatus(ctx context.Context, transport setupssh.Client, helper, directory, host string, ids []string) ([]unitruntime.Status, error) {
	output, err := transport.Run(ctx, []string{helper, "status", "--plan", path.Join(directory, "runtime.json")}, nil)
	if err != nil {
		return nil, errors.New("control dependency runtime observation failed; private output omitted")
	}
	var status unitruntime.Result
	decoder := json.NewDecoder(strings.NewReader(output.Stdout))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&status) != nil || decoder.Decode(new(any)) != io.EOF || status.HostID != host || status.Operation != "status" || len(status.Components) != len(ids) {
		return nil, errors.New("control dependency returned unrelated runtime metadata")
	}
	seen := map[string]bool{}
	for _, component := range status.Components {
		if seen[component.ID] || !slices.Contains(ids, component.ID) || component.State != "paused" && (component.State != "running" || !component.Ready || component.PID <= 1) {
			return nil, errors.New("control dependency component is not ready")
		}
		seen[component.ID] = true
	}
	return status.Components, nil
}

// Control startup requires Storage, and Collector additionally needs egress.
// Fleet-wide host/Access/Trade preparation belongs to the outer bootstrap.
func checkControlDependencies(ctx context.Context, snapshot *setupconfig.Snapshot, root *os.Root, components []string, options CoreOptions) error {
	profiles := []string{}
	if slices.ContainsFunc(components, func(id string) bool {
		return slices.Contains([]string{"monitor", "collector", "factor-mgr", "archive-mgr", "strategy"}, id)
	}) {
		profiles = append(profiles, "storage")
	}
	if slices.Contains(components, "collector") {
		profiles = append(profiles, "egress-proxy")
	}
	for _, profile := range profiles {
		found := false
		for _, host := range snapshot.Manifest.Hosts() {
			if _, err := unitComponents(snapshot.Manifest, host.Name, profile); err != nil {
				continue
			}
			found = true
			if _, err := root.Lstat(filepath.Join("units", host.Name, profile, "operation.json")); err != nil {
				return errors.New("full control requires the original native Storage and egress operations")
			}
			// Reuse the exact deploy transaction to finish or repair its original
			// request. A stale ready PID alone cannot prove that the desired
			// software, configuration and material were successfully activated.
			result, err := DeployUnit(ctx, snapshot, UnitOptions{CoreOptions: options, HostID: host.Name, Profile: profile})
			if err != nil {
				return err
			}
			if result.Stage != profile+"-ready" {
				return errors.New("required control startup dependencies cannot remain paused")
			}
		}
		if !found {
			return errors.New("full control is missing a required Storage or egress placement")
		}
	}
	return nil
}
