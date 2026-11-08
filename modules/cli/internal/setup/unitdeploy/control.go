package unitdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbootstrap"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"gopkg.in/yaml.v3"
)

type ControlOptions struct {
	DeployOptions
	// Release 非空时升级 control：使用该版本的 host 与 control 软件，不顺带部署 Storage 与出口单元。
	Release string
}

// BootstrapControl promotes the original admin software to every control
// deployment. It does not rerun the admin-only request or replace its receipt.
func BootstrapControl(ctx context.Context, snapshot *setupconfig.Snapshot, options ControlOptions) (BootstrapResult, error) {
	return bootstrap(ctx, snapshot, options.DeployOptions, &options)
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
	if options.Release != "" {
		if err := checkControlDependencyRecords(snapshot, root, components); err != nil {
			return request, nil, err
		}
	} else if err := checkControlDependencies(snapshot, root, components); err != nil {
		return request, nil, err
	}
	temporary, err := os.MkdirTemp(root.Name(), "control-templates-")
	if err != nil {
		return request, nil, err
	}
	defer os.RemoveAll(temporary)
	directory, err := controlTemplates(ctx, transport, request, software, components, temporary)
	if err != nil {
		return request, nil, err
	}
	overrides, err := controlConfigurations(snapshot, directory, components)
	return request, overrides, err
}

// controlTemplates 取 control 配置模板。下载的软件不在这台电脑上，从目标机的软件包里读，按清单校验。
func controlTemplates(ctx context.Context, transport setupssh.Client, request unitbootstrap.Request, software unitpackage.Result, components []string, temporary string) (string, error) {
	if software.Archive != "" {
		extraction, err := unitpackage.Extract(ctx, unitpackage.ExtractOptions{Archive: software.Archive, ExpectedSHA256: software.SHA256, Profile: "control", GOOS: "linux", GOARCH: software.Manifest.GOARCH, Destination: filepath.Join(temporary, "software")})
		return extraction.Directory, err
	}
	names := []string{}
	for _, id := range components {
		if slices.Contains([]string{"console-proxy", "monitor", "collector"}, id) {
			names = append(names, id+"/config/app.yaml")
		}
	}
	stage := path.Join(request.DeploymentRoot, "bootstrap-input/templates", strings.TrimPrefix(software.SHA256, "sha256:"))
	arguments := append([]string{"sh", "-c", `set -eu
stage=$1; archive=$2; shift 2
rm -rf -- "$stage"
mkdir -p -m 0700 "$stage"
tar -xzf "$archive" -C "$stage" --no-same-owner --no-same-permissions -- "$@"`, "moox-templates", stage, request.Control.Archive}, names...)
	if output, err := transport.Run(ctx, arguments, nil); err != nil {
		return "", remoteFailure("读取管理台配置模板", err, output)
	}
	defer transport.Run(ctx, []string{"rm", "-rf", "--", stage}, nil)
	directory := filepath.Join(temporary, "templates")
	for _, name := range names {
		var content bytes.Buffer
		if _, err := transport.Download(ctx, path.Join(stage, name), &content); err != nil {
			return "", fmt.Errorf("读取管理台配置模板 %s: %w", name, err)
		}
		i := slices.IndexFunc(software.Manifest.Files, func(file unitpackage.File) bool { return file.Path == name })
		if i < 0 || software.Manifest.Files[i].SHA256 != "sha256:"+sha(content.Bytes()) {
			return "", fmt.Errorf("管理台配置模板 %s 和清单不一致", name)
		}
		local := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(local), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(local, content.Bytes(), 0o600); err != nil {
			return "", err
		}
	}
	return directory, nil
}

// Templates come from the independently verified original package, never
// from files that may have changed in the worktree after the admin build.
func controlConfigurations(snapshot *setupconfig.Snapshot, directory string, components []string) (map[string][]byte, error) {
	result := map[string][]byte{}
	control := snapshot.Manifest.ControlHost()
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
		case "monitor":
			deployment, err := mapping("deployment")
			if err != nil {
				return nil, err
			}
			// 控制主机上的组件由同机的 Monitor 探测，直接连回环地址（公网地址从本机常常访问不通）。
			deployment["local_host_id"] = control.Name
			if slices.Contains(components, "console-proxy") {
				endpoint := (&url.URL{Scheme: "https", Host: net.JoinHostPort(control.Address, "9527"), Path: "/"}).String()
				target := map[string]string{"url": endpoint, "connect_address": net.JoinHostPort(control.Address, "9527"), "server_name": control.Address, "trust_mode": "internal", "ca_file": "../../console-proxy/certs/caddy/root.crt", "ca_baseline": "../../console-proxy/data/caddy/internal-ca.sha256"}
				deployment["https"] = map[string]any{"console-proxy": target}
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
			node := snapshot.Manifest.ComponentHost("storage-node")
			if node == "" {
				return nil, errors.New("Collector requires a registered Storage node deployment")
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
// 每个升级版本各自一条 control-operations/<版本>.json。首次 control 成功前可以改配置重试，
// 配置的部署部分由命令层核对；成功后和升级一样，同一版本重试必须是同一请求。
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
	raw = append(raw, '\n')
	if release == "" {
		if _, err := root.Lstat("control-ready.json"); os.IsNotExist(err) {
			return fsutil.WritePrivate(root, name, raw, true)
		} else if err != nil {
			return err
		}
	}
	return persistPrivateBytes(root, name, raw)
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
	// 不配海外机时没有出口代理，只做 A 股、港股、美股的部署也要能装完。
	if slices.Contains(components, "collector") && snapshot.Manifest.HasEgressProxy() {
		profiles = append(profiles, "egress-proxy")
	}
	for _, profile := range profiles {
		found := false
		for _, host := range snapshot.Manifest.Hosts() {
			if _, err := unitComponents(snapshot.Manifest, host.Name, profile); err != nil {
				continue
			}
			if !unitDeploymentRecorded(root, host.Name, profile) {
				return fmt.Errorf("control upgrade requires an existing %s deployment on %s", profile, host.Name)
			}
			found = true
		}
		if !found {
			return errors.New("control upgrade is missing a required Storage or egress deployment")
		}
	}
	return nil
}

// unitDeploymentRecorded 报告单元已有部署记录：首次安装的 operation.json，或任一版本安装的 releases/<版本>/operation.json。
func unitDeploymentRecorded(root *os.Root, host, profile string) bool {
	base := filepath.Join("units", host, profile)
	if _, err := root.Lstat(filepath.Join(base, "operation.json")); err == nil {
		return true
	}
	entries, err := fs.ReadDir(root.FS(), filepath.Join(base, "releases"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if _, err := root.Lstat(filepath.Join(base, "releases", entry.Name(), "operation.json")); entry.IsDir() && err == nil {
			return true
		}
	}
	return false
}

func activeControlStatus(ctx context.Context, transport setupssh.Client, helper, directory, host string, ids []string) ([]unitruntime.Status, error) {
	output, err := transport.Run(ctx, []string{helper, "status", "--plan", path.Join(directory, "runtime.json")}, nil)
	if err != nil {
		return nil, remoteFailure("查询管理台组件状态", err, output)
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

// checkControlDependencies 确认 control 启动依赖的单元都已就绪：Storage 总是需要，Collector 还要出口代理。
// 只读 ready.json，单元没好时要用户修好再来，不在这里重新部署。
func checkControlDependencies(snapshot *setupconfig.Snapshot, root *os.Root, components []string) error {
	profiles := []string{}
	if slices.ContainsFunc(components, func(id string) bool {
		return slices.Contains([]string{"monitor", "collector", "factor-mgr", "archive-mgr", "strategy"}, id)
	}) {
		profiles = append(profiles, "storage")
	}
	// 不配海外机时没有出口代理，只做 A 股、港股、美股的部署也要能装完。
	if slices.Contains(components, "collector") && snapshot.Manifest.HasEgressProxy() {
		profiles = append(profiles, "egress-proxy")
	}
	for _, profile := range profiles {
		found := false
		for _, host := range snapshot.Manifest.Hosts() {
			if _, err := unitComponents(snapshot.Manifest, host.Name, profile); err != nil {
				continue
			}
			found = true
			var ready struct {
				Stage string `json:"stage"`
			}
			if err := readJSON(root, filepath.Join("units", host.Name, profile, unitReadyFile), 4096, &ready); err != nil || ready.Stage != profile+"-ready" {
				unitRoot := path.Join(snapshot.Manifest.Paths.DeployRoot, profile)
				if profile == "storage" {
					unitRoot = snapshot.Manifest.Paths.StorageRoot
				}
				return fmt.Errorf("control_dependency_not_ready: %s 上的 %s 单元没有就绪，先运行 setup deploy-unit --host %s --profile %s；服务被暂停时先在该主机运行 %s，再重新部署这个单元", host.Name, profile, host.Name, profile, path.Join(unitRoot, "current", "resume.sh"))
			}
		}
		if !found {
			return errors.New("full control is missing a required Storage or egress deployment")
		}
	}
	return nil
}

// ConfigDrift 比对 Storage、出口单元最新一次部署用的配置，变了就说明要怎么生效。这些配置只在单元部署时写入。
func ConfigDrift(snapshot *setupconfig.Snapshot, stateDir string) ([]string, error) {
	warnings := []string{}
	for _, profile := range []string{"storage", "egress-proxy"} {
		configuration, err := unitConfiguration(snapshot, profile)
		if err != nil {
			return nil, err
		}
		for _, host := range snapshot.Manifest.Hosts() {
			if _, err := unitComponents(snapshot.Manifest, host.Name, profile); err != nil {
				continue
			}
			deployed, ok := latestUnitConfiguration(stateDir, host.Name, profile)
			if !ok || deployed == "sha256:"+sha(configuration) {
				continue
			}
			warning := fmt.Sprintf("%s 上的 %s 配置和部署时不同，要发新版本才生效：setup release fetch --release <新ID>，再 setup deploy-unit --host %s --profile %s --release <新ID>", host.Name, profile, host.Name, profile)
			if profile == "egress-proxy" {
				warning += "；出口代理配置也进了 Collector，还要 setup bootstrap --stage control --release <新ID>"
			}
			warnings = append(warnings, warning)
		}
	}
	return warnings, nil
}

// latestUnitConfiguration 读单元最新一次部署记录里的配置摘要，没有记录时 ok 为 false。
func latestUnitConfiguration(stateDir, host, profile string) (string, bool) {
	base := filepath.Join(stateDir, "units", host, profile)
	paths, _ := filepath.Glob(filepath.Join(base, "releases", "*", "operation.json"))
	paths = append(paths, filepath.Join(base, "operation.json"))
	latest, digest := time.Time{}, ""
	for _, name := range paths {
		info, err := os.Stat(name)
		if err != nil || info.ModTime().Before(latest) {
			continue
		}
		raw, err := os.ReadFile(name)
		var operation unitOperation
		if err != nil || json.Unmarshal(raw, &operation) != nil {
			continue
		}
		latest, digest = info.ModTime(), operation.ConfigurationSHA256
	}
	return digest, digest != ""
}
