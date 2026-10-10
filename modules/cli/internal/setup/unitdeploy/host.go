package unitdeploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbootstrap"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type HostOptions struct {
	CoreOptions
	HostID string
}
type HostResult struct {
	Stage      string                 `json:"stage"`
	Deployment unitinstall.Deployment `json:"deployment"`
}

type unitOperation struct {
	Profile             string                      `json:"profile"`
	Components          []string                    `json:"components"`
	ConfigurationSHA256 string                      `json:"configuration_sha256"`
	EnvironmentSHA256   string                      `json:"environment_sha256"`
	Version             int                         `json:"version"`
	Source              unitbootstrap.ExportRequest `json:"source"`
	Export              *unitbootstrap.ExportResult `json:"export,omitempty"`
	Package             unitpackage.Result          `json:"package"`
	Helper              string                      `json:"helper"`
	HelperSHA256        string                      `json:"helper_sha256"`
	Prefix              string                      `json:"prefix"`
}

func readJSON(root *os.Root, name string, max int64, out any) error {
	if _, err := root.Lstat(name); err != nil {
		return err
	}
	raw, err := fsutil.ReadPrivate(root, name, max)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("native deployment state requires known JSON fields")
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return errors.New("native deployment state is not canonical")
	}
	return nil
}

func saveJSON(root *os.Root, name string, value any, replace bool) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(root, name, append(raw, '\n'), replace)
}

func runtimeBinding(identity runtimeIdentity) string {
	raw, _ := json.Marshal(map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": identity.HealthAccessKey, "MOOX_HEALTH_AUTH_SECRET_KEY": identity.HealthSecret, "MOOX_ADMIN_JWT_SECRET_KEY": identity.JWTSecret})
	return "sha256:" + sha(raw)
}

// DeployHost installs the two host components after core initialization. It
// reads the operator manifest only through the caller's trusted snapshot.
func DeployHost(ctx context.Context, snapshot *setupconfig.Snapshot, options HostOptions) (HostResult, error) {
	return DeployUnit(ctx, snapshot, UnitOptions{CoreOptions: options.CoreOptions, HostID: options.HostID, Profile: "host"})
}

type UnitOptions struct {
	CoreOptions
	HostID  string
	Profile string
}
type UnitResult = HostResult

func DeployUnit(ctx context.Context, snapshot *setupconfig.Snapshot, options UnitOptions) (UnitResult, error) {
	var result HostResult
	if snapshot == nil || snapshot.VerifyUnchanged() != nil {
		return result, errors.New("unit deployment requires an unchanged setup snapshot")
	}
	manifest := snapshot.Manifest
	if !slices.Contains([]string{"host", "access", "egress-proxy", "trade", "storage"}, options.Profile) {
		return result, errors.New("unsupported native deployment unit")
	}
	unitRoot := path.Join(manifest.Paths.DeployRoot, options.Profile)
	if options.Profile == "storage" {
		unitRoot = manifest.Paths.StorageRoot
	}
	if err := fsutil.ValidateUnitRoots(manifest.Paths.DeployRoot, path.Join(manifest.Paths.DeployRoot, "host"), manifest.Paths.ControlRoot); err != nil {
		return result, err
	}
	if options.Profile != "host" {
		if err := fsutil.ValidateUnitRoots(manifest.Paths.DeployRoot, path.Join(manifest.Paths.DeployRoot, "host"), manifest.Paths.ControlRoot, unitRoot); err != nil {
			return result, err
		}
	}
	components, err := unitComponents(manifest, options.HostID, options.Profile)
	if err != nil {
		return result, err
	}
	configuration, err := unitConfiguration(snapshot, options.Profile)
	if err != nil {
		return result, err
	}
	configurationSHA := "sha256:" + sha(configuration)
	if !slices.ContainsFunc(manifest.Hosts(), func(host setupconfig.Host) bool { return host.Name == options.HostID }) {
		return result, errors.New("unknown deployment host")
	}
	root, err := fsutil.OpenPhysicalRoot(options.StateDirectory, true)
	if err != nil {
		return result, err
	}
	defer root.Close()
	// Unit deployment must never generate replacement fleet credentials when
	// the operator loses the state created by bootstrap core.
	if _, err := fsutil.ReadPrivate(root, "runtime-identity.json", 4096); err != nil {
		return result, errors.New("unit deployment requires the original core state directory")
	}
	control := manifest.ControlHost()
	var ready CoreResult
	if err := readJSON(root, "core-ready.json", 128<<10, &ready); err != nil || ready.Stage != "core-ready" || ready.Bootstrap.HostID != control.Name || ready.Bootstrap.Phase != "complete" {
		return result, errors.New("unit deployment requires the original core state directory and completed core receipt")
	}
	if !insideState(manifest.Paths.ControlRoot, ready.Bootstrap.ControlDirectory) || !insideState(path.Join(manifest.Paths.DeployRoot, "host"), ready.Bootstrap.HostDirectory) {
		return result, errors.New("unit deployment requires the control and host roots from its original core receipt")
	}
	identity, err := loadIdentity(root, control, manifest.Paths.DeployRoot)
	if err != nil {
		return result, err
	}
	environment, err := unitEnvironment(manifest, identity, options.HostID, components)
	if err != nil {
		return result, err
	}
	environmentRaw, err := json.Marshal(environment)
	if err != nil {
		return result, err
	}
	environmentSHA := "sha256:" + sha(environmentRaw)
	pin, err := unitbundle.ReadOperatorPin(options.OperatorDirectory)
	if err != nil || pin.ControlHostID != control.Name {
		return result, errors.New("unit deployment requires its original operator bootstrap pin")
	}
	host := manifest.HostByID(options.HostID)
	material := unitbundle.Options{HostID: host.Name, ControlHostID: control.Name, Address: host.Address, PrivateAddress: host.PrivateAddress, ControlAddress: control.Address, Components: slices.Clone(manifest.Placements[host.Name]), ExpectedCA: pin.CA}
	slices.Sort(material.Components)
	request := unitbootstrap.ExportRequest{Version: 1, DeploymentRoot: manifest.Paths.DeployRoot, ControlUnitRoot: manifest.Paths.ControlRoot, RuntimeSHA256: runtimeBinding(identity), Target: material, Roles: unitinstall.EventBusRoles(components)}
	stateDirectory := filepath.Join(root.Name(), "units", host.Name, options.Profile)
	if options.Profile == "host" {
		stateDirectory = filepath.Join(root.Name(), "hosts", host.Name)
	}
	state, err := privateRoot(stateDirectory)
	if err != nil {
		return result, err
	}
	defer state.Close()
	var operation unitOperation
	operationErr := readJSON(state, "operation.json", 2<<20, &operation)
	if operationErr == nil {
		request.ExportID = operation.Source.ExportID
		old, _ := json.Marshal(operation.Source)
		expected, _ := json.Marshal(request)
		if operation.Version != 1 || operation.Profile != options.Profile || !slices.Equal(operation.Components, components) || operation.ConfigurationSHA256 != configurationSHA || operation.EnvironmentSHA256 != environmentSHA || !bytes.Equal(old, expected) {
			return result, errors.New("unit retry requires the original topology, configuration, runtime identity and CA")
		}
	} else if !os.IsNotExist(operationErr) {
		return result, operationErr
	}
	options.SSH.OutputLimit = 128 << 10
	if options.SSH.Timeout == 0 {
		options.SSH.Timeout = 15 * time.Second
	}
	dial := func(h setupconfig.Host) (setupssh.Client, error) {
		return setupssh.Dial(ctx, setupssh.Target{Name: h.Name, Address: h.Address, Port: h.Port, Username: h.Username}, h.Password, options.SSH)
	}
	target, err := dial(host)
	if err != nil {
		return result, err
	}
	defer target.Close()
	arch, err := targetArchitecture(ctx, target)
	if err != nil {
		return result, err
	}
	source, err := dial(control)
	if err != nil {
		return result, err
	}
	defer source.Close()
	controlArch, err := targetArchitecture(ctx, source)
	if err != nil {
		return result, err
	}
	core, exists, err := readCoreSoftware(ctx, root, controlArch)
	if err != nil || !exists {
		return result, errors.New("unit deployment requires the original verified core software")
	}
	if os.IsNotExist(operationErr) {
		software, helper, err := prepareUnitSoftware(ctx, root, state, options.CoreOptions, arch, options.Profile, core)
		if err != nil {
			return result, err
		}
		helperSHA, err := fileSHA(helper)
		if err != nil {
			return result, err
		}
		request.ExportID = options.Profile + "-" + rand.Text()
		operation = unitOperation{Profile: options.Profile, Components: components, ConfigurationSHA256: configurationSHA, EnvironmentSHA256: environmentSHA, Version: 1, Source: request, Package: software, Helper: helper, HelperSHA256: helperSHA, Prefix: options.Profile + "-" + rand.Text()[:16]}
		if err := saveJSON(state, "operation.json", operation, false); err != nil {
			return result, err
		}
	}
	verified, err := unitpackage.Inspect(ctx, operation.Package.Archive)
	if err != nil || verified.SHA256 != operation.Package.SHA256 || verified.Manifest.GOARCH != arch || verified.Manifest.Profile != options.Profile || !insideState(root.Name(), operation.Package.Archive) || !insideState(root.Name(), operation.Helper) {
		return result, errors.New("original unit software changed or does not match target architecture")
	}
	helperSHA, err := fileSHA(operation.Helper)
	if err != nil || helperSHA != operation.HelperSHA256 || options.Profile == "host" && !slices.ContainsFunc(verified.Manifest.Files, func(file unitpackage.File) bool {
		return file.Path == "bin/moox-runtime" && file.SHA256 == "sha256:"+helperSHA
	}) {
		return result, errors.New("unit runtime helper differs from its verified software")
	}
	if operation.Export == nil {
		gateway, err := gatewayio.OpenWithIdentity(ctx, snapshot, filepath.Join(options.OperatorDirectory, "gateway-client.yaml"), options.SSH)
		if err != nil {
			return result, err
		}
		syncErr := setupclient.New(gateway).SyncHostPlacements(ctx, snapshot, host.Name)
		closeErr := gateway.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return result, err
		}
		raw, err := json.Marshal(operation.Source)
		if err != nil {
			return result, err
		}
		raw = append(raw, '\n')
		remoteHelper := path.Join(manifest.Paths.DeployRoot, "bootstrap-input/runtime", core.HelperSHA256, "moox-runtime")
		remoteRequest := path.Join(manifest.Paths.DeployRoot, "bootstrap-input/requests", sha(raw), "export-host.json")
		if err := snapshot.VerifyUnchanged(); err != nil {
			return result, errors.New("config_changed")
		}
		if err := uploadFile(ctx, source, manifest.Paths.DeployRoot, core.Helper, remoteHelper, core.HelperSHA256, 0o700); err != nil {
			return result, err
		}
		if err := uploadBytes(ctx, source, manifest.Paths.DeployRoot, raw, remoteRequest, 0o600); err != nil {
			return result, err
		}
		output, err := source.Run(ctx, []string{remoteHelper, "export-host", "--request", remoteRequest}, nil)
		if err != nil {
			return result, errors.New("host identity export observation failed; retry original state; private output omitted")
		}
		var exported unitbootstrap.ExportResult
		decoder := json.NewDecoder(strings.NewReader(output.Stdout))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&exported) != nil || decoder.Decode(new(any)) != io.EOF || exported.Version != 1 || exported.RequestSHA256 != "sha256:"+sha(bytes.TrimSuffix(raw, []byte{'\n'})) || path.Dir(exported.Host.BundleDir) != path.Join(manifest.Paths.DeployRoot, "material-exports", operation.Source.ExportID) || exported.EventBus.OutputDir != path.Join(manifest.Paths.DeployRoot, "material-exports", operation.Source.ExportID, "eventbus") {
			return result, errors.New("control returned invalid host export metadata")
		}
		operation.Export = &exported
		if err := saveJSON(state, "operation.json", operation, true); err != nil {
			return result, err
		}
	}
	exported := operation.Export
	material.ExpectedHash = exported.Host.ExpectedHash
	localBundle := filepath.Join(state.Name(), "identity")
	if _, err := os.Lstat(localBundle); os.IsNotExist(err) {
		if _, err := unitbundle.Fetch(ctx, source.Download, exported.Host, material, localBundle); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	if _, err := unitbundle.Load(ctx, localBundle, material); err != nil {
		return result, err
	}
	localClients := filepath.Join(state.Name(), "eventbus")
	if _, err := os.Lstat(localClients); os.IsNotExist(err) {
		if err := unitbundle.FetchEventBusClients(ctx, source.Download, exported.EventBus, operation.Source.Roles, localClients); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	if err := unitbundle.LoadEventBusClientsAt(ctx, exported.EventBus, operation.Source.Roles, localClients); err != nil {
		return result, err
	}
	remoteBase := path.Join(manifest.Paths.DeployRoot, "bootstrap-input/host-deploy", operation.Source.ExportID)
	prepare := unitinstall.PrepareOptions{Archive: path.Join(remoteBase, options.Profile+".tar.gz"), SHA256: operation.Package.SHA256, Profile: options.Profile, DeploymentRoot: manifest.Paths.DeployRoot, UnitRoot: unitRoot, HostUnitRoot: path.Join(manifest.Paths.DeployRoot, "host"), ReleaseID: operation.Prefix, MaterialDirectory: path.Join(remoteBase, "identity"), MaterialOptions: material, Components: components, Environment: environment, Overrides: map[string]string{}, EventBusDirectory: path.Join(remoteBase, "eventbus"), EventBusURL: "tls://" + net.JoinHostPort(control.Address, "4222")}
	overrideHashes := map[string]string{}
	if len(configuration) > 0 {
		local := filepath.Join(state.Name(), "configuration.yaml")
		if _, err := state.Lstat("configuration.yaml"); os.IsNotExist(err) {
			if err := fsutil.WritePrivate(state, "configuration.yaml", configuration, false); err != nil {
				return result, err
			}
		} else if err != nil {
			return result, err
		}
		digest, err := fileSHA(local)
		if err != nil || "sha256:"+digest != configurationSHA {
			return result, errors.New("original rendered unit configuration changed")
		}
		name := options.Profile + "/config/app.yaml"
		names := []string{name}
		if options.Profile == "storage" {
			names = nil
			for _, id := range components {
				names = append(names, id+"/config/storage-policy.json")
			}
		}
		for _, name := range names {
			prepare.Overrides[name] = path.Join(remoteBase, "configuration.yaml")
			overrideHashes[name] = configurationSHA
		}
	}
	raw, err := json.Marshal(prepare)
	if err != nil {
		return result, err
	}
	raw = append(raw, '\n')
	expectedRequest, err := unitinstall.DeploymentRequestHash(prepare, exported.Host, exported.EventBus, overrideHashes)
	if err != nil {
		return result, err
	}
	if err := persistRequest(root, sha(raw), raw); err != nil {
		return result, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return result, errors.New("config_changed")
	}
	if err := prepareTarget(ctx, target, manifest.Paths.DeployRoot, prepare.UnitRoot); err != nil {
		return result, err
	}
	if err := uploadFile(ctx, target, manifest.Paths.DeployRoot, operation.Package.Archive, prepare.Archive, strings.TrimPrefix(prepare.SHA256, "sha256:"), 0o600); err != nil {
		return result, err
	}
	for _, group := range []struct {
		local, remote string
		files         []hostbundle.File
		metadata      string
	}{{localBundle, prepare.MaterialDirectory, exported.Host.Files, "bundle.json"}, {localClients, prepare.EventBusDirectory, exported.EventBus.Files, "clients.json"}} {
		files := slices.Clone(group.files)
		metadataSHA, err := fileSHA(filepath.Join(group.local, group.metadata))
		if err != nil {
			return result, err
		}
		files = append(files, hostbundle.File{Path: group.metadata, SHA256: metadataSHA})
		for _, file := range files {
			if err := uploadFile(ctx, target, manifest.Paths.DeployRoot, filepath.Join(group.local, file.Path), path.Join(group.remote, file.Path), file.SHA256, 0o600); err != nil {
				return result, err
			}
		}
	}
	if len(configuration) > 0 {
		if err := uploadBytes(ctx, target, manifest.Paths.DeployRoot, configuration, path.Join(remoteBase, "configuration.yaml"), 0o600); err != nil {
			return result, err
		}
	}
	remoteHelper := path.Join(manifest.Paths.DeployRoot, "bootstrap-input/runtime", operation.HelperSHA256, "moox-runtime")
	if err := uploadFile(ctx, target, manifest.Paths.DeployRoot, operation.Helper, remoteHelper, operation.HelperSHA256, 0o700); err != nil {
		return result, err
	}
	remoteRequest := path.Join(remoteBase, "request.json")
	if err := uploadBytes(ctx, target, manifest.Paths.DeployRoot, raw, remoteRequest, 0o600); err != nil {
		return result, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return result, errors.New("config_changed")
	}
	output, err := target.Run(ctx, []string{remoteHelper, "deploy", "--request", remoteRequest}, nil)
	if err != nil {
		return result, errors.New("unit deployment observation failed; retry original state; target journal retained and private output omitted")
	}
	decoder := json.NewDecoder(strings.NewReader(output.Stdout))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result.Deployment) != nil || decoder.Decode(new(any)) != io.EOF || result.Deployment.Version != 1 || result.Deployment.HostID != host.Name || result.Deployment.RequestSHA256 != expectedRequest || result.Deployment.Phase != "complete" || path.Dir(result.Deployment.Directory) != path.Join(prepare.UnitRoot, "releases") || len(result.Deployment.Components) != len(prepare.Components) {
		return HostResult{}, errors.New("target returned invalid public unit deployment result")
	}
	result.Stage = options.Profile + "-ready"
	for i, component := range result.Deployment.Components {
		if component.ID != prepare.Components[i] {
			return HostResult{}, errors.New("target returned an unrelated unit component")
		}
		if component.State == "paused" {
			result.Stage = options.Profile + "-paused"
		} else if component.State != "running" || !component.Ready {
			return HostResult{}, errors.New("deployed unit component is not ready")
		}
	}
	return result, nil
}
