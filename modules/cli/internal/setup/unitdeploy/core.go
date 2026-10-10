// Package unitdeploy runs deployment workflows on the operator's native CLI.
// Only normalized inputs and prebuilt Linux software cross verified SSH.
package unitdeploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
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
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type CoreOptions struct {
	RepositoryRoot    string
	BinaryDirectory   string // Empty builds pure Go and frontend locally.
	StateDirectory    string // Persistent private state; retain across retries/upgrades.
	OperatorDirectory string
	Log               io.Writer
	SSH               setupssh.Options
}

type CoreResult struct {
	Stage          string               `json:"stage"`
	Bootstrap      unitbootstrap.Result `json:"bootstrap"`
	OperatorConfig string               `json:"operator_config"`
}

type runtimeIdentity struct {
	Version              int    `json:"version"`
	HostID               string `json:"host_id"`
	Address              string `json:"address"`
	DeploymentRoot       string `json:"deployment_root"`
	HealthAccessKey      string `json:"health_access_key"`
	HealthSecret         string `json:"health_secret"`
	JWTSecret            string `json:"jwt_secret"`
	StorageNodeSecret    string `json:"storage_node_secret,omitempty"`
	StoragePrimarySecret string `json:"storage_primary_secret,omitempty"`
	StorageViewSecret    string `json:"storage_view_secret,omitempty"`
}

func (runtimeIdentity) String() string     { return "MooXRuntimeIdentity{private inputs omitted}" }
func (r runtimeIdentity) GoString() string { return r.String() }

func sha(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func privateRoot(directory string) (*os.Root, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == string(filepath.Separator) {
		return nil, errors.New("deployment state requires a clean absolute private directory")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	return fsutil.OpenPhysicalRoot(directory, true)
}

func loadIdentity(root *os.Root, host setupconfig.Host, deployment string) (runtimeIdentity, error) {
	identity := runtimeIdentity{Version: 1, HostID: host.Name, Address: host.Address, DeploymentRoot: deployment}
	if _, err := root.Lstat("runtime-identity.json"); os.IsNotExist(err) {
		identity.HealthAccessKey, identity.HealthSecret, identity.JWTSecret = "monitor-"+rand.Text(), rand.Text()+rand.Text(), rand.Text()+rand.Text()
		identity.StorageNodeSecret, identity.StoragePrimarySecret, identity.StorageViewSecret = rand.Text()+rand.Text(), rand.Text()+rand.Text(), rand.Text()+rand.Text()
		raw, err := json.Marshal(identity)
		if err != nil {
			return identity, err
		}
		if err := fsutil.WritePrivate(root, "runtime-identity.json", append(raw, '\n'), false); err != nil {
			return identity, err
		}
	} else if err != nil {
		return identity, err
	}
	raw, err := fsutil.ReadPrivate(root, "runtime-identity.json", 4096)
	if err != nil {
		return identity, err
	}
	var stored runtimeIdentity
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&stored) != nil || decoder.Decode(new(any)) != io.EOF || stored.Version != 1 || stored.HostID != host.Name || stored.Address != host.Address || stored.DeploymentRoot != deployment || len(stored.HealthAccessKey) < 16 || len(stored.HealthSecret) < 32 || len(stored.JWTSecret) < 32 {
		return identity, errors.New("private runtime identity does not match this deployment")
	}
	if (stored.StorageNodeSecret != "" || stored.StoragePrimarySecret != "" || stored.StorageViewSecret != "") && (len(stored.StorageNodeSecret) < 32 || len(stored.StoragePrimarySecret) < 32 || len(stored.StorageViewSecret) < 32) {
		return identity, errors.New("private Storage identity is incomplete")
	}
	return stored, nil
}

func coreRequest(manifest setupconfig.Manifest, identity runtimeIdentity, host, control unitpackage.Result) unitbootstrap.Request {
	topology := hostbundle.Topology{Version: 1, ControlHostID: manifest.ControlHost().Name}
	for _, h := range manifest.Hosts() {
		components := slices.Clone(manifest.Placements[h.Name])
		slices.Sort(components)
		topology.Hosts = append(topology.Hosts, hostbundle.Host{HostID: h.Name, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Components: components})
	}
	health := map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": identity.HealthAccessKey, "MOOX_HEALTH_AUTH_SECRET_KEY": identity.HealthSecret}
	unit := func(result unitpackage.Result, name string, components []string) unitbootstrap.Unit {
		env := map[string]map[string]string{}
		for _, id := range components {
			env[id] = maps.Clone(health)
		}
		return unitbootstrap.Unit{Archive: path.Join(manifest.Paths.DeployRoot, "bootstrap-input/packages", strings.TrimPrefix(result.SHA256, "sha256:"), name+".tar.gz"), SHA256: result.SHA256, UnitRoot: path.Join(manifest.Paths.DeployRoot, name), Environment: env, Overrides: map[string]string{}, Components: components}
	}
	request := unitbootstrap.Request{Version: 1, DeploymentRoot: manifest.Paths.DeployRoot, Topology: topology, Host: unit(host, "host", []string{"host-gateway", "host-agent"}), Control: unit(control, "control", []string{"admin", "eventbus"})}
	request.Control.Environment["admin"]["MOOX_ADMIN_JWT_SECRET_KEY"] = identity.JWTSecret
	return request
}

// BootstrapCore establishes Admin, EventBus and both host components. Its
// result deliberately says core-ready: fleet services and Console Proxy are
// installed by the outer workflow once Storage and other dependencies exist.
func BootstrapCore(ctx context.Context, snapshot *setupconfig.Snapshot, options CoreOptions) (CoreResult, error) {
	var result CoreResult
	if snapshot == nil || snapshot.VerifyUnchanged() != nil {
		return result, errors.New("bootstrap requires an unchanged private setup snapshot")
	}
	root, err := privateRoot(options.StateDirectory)
	if err != nil {
		return result, err
	}
	defer root.Close()
	operatorRoot, err := privateRoot(options.OperatorDirectory)
	if err != nil {
		return result, err
	}
	operatorRoot.Close()
	host := snapshot.Manifest.ControlHost()
	identity, err := loadIdentity(root, host, snapshot.Manifest.Paths.DeployRoot)
	if err != nil {
		return result, err
	}
	options.SSH.OutputLimit = 128 << 10
	if options.SSH.Timeout == 0 {
		options.SSH.Timeout = 15 * time.Second
	}
	transport, err := setupssh.Dial(ctx, setupssh.Target{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username}, host.Password, options.SSH)
	if err != nil {
		return result, err
	}
	defer transport.Close()
	arch, err := targetArchitecture(ctx, transport)
	if err != nil {
		return result, err
	}
	packages, helper, err := prepareCoreSoftware(ctx, root, options, arch)
	if err != nil {
		return result, err
	}
	request := coreRequest(snapshot.Manifest, identity, packages[0], packages[1])
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	raw = append(raw, '\n')
	expectedRequest, err := unitbootstrap.RequestHash(request, [2]map[string][]byte{})
	if err != nil {
		return result, err
	}
	requestPath := path.Join(request.DeploymentRoot, "bootstrap-input/requests", sha(raw), "request.json")
	if err := persistRequest(root, sha(raw), raw); err != nil {
		return result, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return result, errors.New("config_changed")
	}
	if err := prepareTarget(ctx, transport, request.DeploymentRoot); err != nil {
		return result, err
	}
	for i, unit := range []unitbootstrap.Unit{request.Host, request.Control} {
		if err := uploadFile(ctx, transport, request.DeploymentRoot, packages[i].Archive, unit.Archive, strings.TrimPrefix(unit.SHA256, "sha256:"), 0o600); err != nil {
			return result, err
		}
	}
	helperSHA, err := fileSHA(helper)
	if err != nil {
		return result, err
	}
	remoteHelper := path.Join(request.DeploymentRoot, "bootstrap-input/runtime", helperSHA, "moox-runtime")
	if err := uploadFile(ctx, transport, request.DeploymentRoot, helper, remoteHelper, helperSHA, 0o700); err != nil {
		return result, err
	}
	if err := uploadBytes(ctx, transport, request.DeploymentRoot, raw, requestPath, 0o600); err != nil {
		return result, err
	}
	if err := snapshot.VerifyUnchanged(); err != nil {
		return result, errors.New("config_changed")
	}
	output, err := transport.Run(ctx, []string{remoteHelper, "bootstrap", "--request", requestPath}, nil)
	if err != nil {
		// SSH observation failure is not evidence that the target exited. Leave
		// its journal and the exact request intact for a safe explicit retry.
		return result, errors.New("bootstrap observation failed; retry with the original state directory and configuration; target journal retained and private output omitted")
	}
	decoder := json.NewDecoder(strings.NewReader(output.Stdout))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result.Bootstrap) != nil || decoder.Decode(new(any)) != io.EOF || result.Bootstrap.Phase != "complete" || result.Bootstrap.HostID != host.Name || result.Bootstrap.RequestSHA256 != expectedRequest || !insideState(request.Host.UnitRoot, result.Bootstrap.HostDirectory) || !insideState(request.Control.UnitRoot, result.Bootstrap.ControlDirectory) || !insideState(path.Join(request.DeploymentRoot, "bootstrap"), result.Bootstrap.BundleDirectory) {
		return CoreResult{}, errors.New("bootstrap helper returned invalid public result metadata")
	}
	control := request.Topology.Hosts[slices.IndexFunc(request.Topology.Hosts, func(h hostbundle.Host) bool { return h.HostID == host.Name })]
	materialOptions := unitbundle.Options{HostID: host.Name, ControlHostID: host.Name, Address: host.Address, PrivateAddress: host.PrivateAddress, ControlAddress: host.Address, Components: control.Components, ExpectedCA: result.Bootstrap.CAFingerprint, ExpectedHash: result.Bootstrap.ExpectedHash, AllowOperator: true}
	metadata, err := unitbundle.FetchMetadata(ctx, transport.Download, result.Bootstrap.BundleDirectory, materialOptions)
	if err != nil {
		return result, err
	}
	if err := unitbundle.InstallOperator(ctx, transport.Download, metadata, materialOptions, options.OperatorDirectory); err != nil {
		return result, err
	}
	result.Stage, result.OperatorConfig = "core-ready", filepath.Join(options.OperatorDirectory, "gateway-client.yaml")
	if err := saveJSON(root, "core-ready.json", result, true); err != nil {
		return result, err
	}
	return result, nil
}

func persistRequest(root *os.Root, digest string, raw []byte) error {
	name := "requests/" + digest + ".json"
	if _, err := root.Lstat(name); err == nil {
		old, err := fsutil.ReadPrivate(root, name, 4<<20)
		if err != nil || !bytes.Equal(old, raw) {
			return errors.New("persisted bootstrap request changed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return fsutil.WritePrivate(root, name, raw, false)
}
