package unitdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
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
	ProxyCA      unitbootstrap.ProxyCAAuthorization
	FactorPython string // Prepared target interpreter; empty uses python3.
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
	if request.ProxyCA.Create && request.ProxyCA.ImportDirectory != "" || request.ProxyCA.Create && consoleTLSMode(snapshot.Manifest.ControlHost()) == "public" {
		return request, nil, errors.New("proxy CA creation requires internal TLS and cannot be combined with trusted import")
	}
	if err := checkControlDependencies(ctx, snapshot, root, components, options.CoreOptions); err != nil {
		return request, nil, err
	}
	python := ""
	if slices.Contains(components, "factor-mgr") {
		python, err = checkFactorPython(ctx, transport, options.FactorPython)
		if err != nil {
			return request, nil, err
		}
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
	overrides, err := controlConfigurations(snapshot, extraction.Directory, components, python)
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
func controlConfigurations(snapshot *setupconfig.Snapshot, directory string, components []string, python string) (map[string][]byte, error) {
	result := map[string][]byte{}
	control := snapshot.Manifest.ControlHost()
	mode := consoleTLSMode(control)
	for _, id := range components {
		if !slices.Contains([]string{"console-proxy", "monitor", "collector", "factor-mgr"}, id) {
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
			storage["result_data_node_id"] = node + "-storage-node"
		case "factor-mgr":
			if python == "" || !path.IsAbs(python) || path.Clean(python) != python || strings.ContainsAny(python, "\x00\r\n") {
				return nil, errors.New("Factor requires a verified prepared target Python interpreter")
			}
			config, err := mapping("python")
			if err != nil {
				return nil, err
			}
			config["bin"] = python
		}
		result[name], err = yaml.Marshal(configuration)
		if err != nil {
			return nil, errors.New("cannot encode normalized control configuration")
		}
	}
	return result, nil
}

func checkFactorPython(ctx context.Context, transport setupssh.Client, executable string) (string, error) {
	if executable == "" {
		executable = "python3"
	}
	if executable != "python3" && (!path.IsAbs(executable) || path.Clean(executable) != executable) || strings.ContainsAny(executable, "\x00\r\n") {
		return "", errors.New("Factor Python must select python3 or a clean absolute prepared interpreter")
	}
	code := "import json,sys,pandas,numpy; print(json.dumps({'executable':sys.executable,'pandas':pandas.__version__,'numpy':numpy.__version__}))"
	output, err := transport.Run(ctx, []string{executable, "-I", "-c", code}, nil)
	if err != nil {
		return "", errors.New("Factor requires prepared Python with pandas 2.2+ and numpy 2; private preflight output omitted")
	}
	var metadata struct{ Executable, Pandas, Numpy string }
	decoder := json.NewDecoder(strings.NewReader(output.Stdout))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF || !path.IsAbs(metadata.Executable) || path.Clean(metadata.Executable) != metadata.Executable || strings.ContainsAny(metadata.Executable, "\x00\r\n") || !strings.HasPrefix(metadata.Numpy, "2.") {
		return "", errors.New("Factor Python returned invalid public runtime metadata")
	}
	version := strings.Split(metadata.Pandas, ".")
	if len(version) < 2 || version[0] != "2" {
		return "", errors.New("Factor requires pandas 2.2 or newer within major version 2")
	}
	minor, err := strconv.Atoi(version[1])
	if err != nil || minor < 2 {
		return "", errors.New("Factor requires pandas 2.2 or newer within major version 2")
	}
	return metadata.Executable, nil
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

func bindControlRequest(root *os.Root, digest string) error {
	raw, err := json.Marshal(struct {
		Version int    `json:"version"`
		SHA256  string `json:"sha256"`
	}{1, digest})
	if err != nil {
		return err
	}
	return persistPrivateBytes(root, "control-operation.json", append(raw, '\n'))
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
		return slices.Contains([]string{"monitor", "collector", "factor-mgr", "strategy"}, id)
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
