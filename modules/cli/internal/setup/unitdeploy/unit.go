package unitdeploy

import (
	"context"
	"errors"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
)

func unitComponents(manifest setupconfig.Manifest, hostID, profile string) ([]string, error) {
	if profile == "host" {
		return []string{"host-gateway", "host-agent"}, nil
	}
	available, err := unitpackage.Components(profile)
	if err != nil {
		return nil, err
	}
	var selected []string
	for _, component := range available {
		if slices.Contains(manifest.Placements[hostID], component.ID) {
			selected = append(selected, component.ID)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("requested software unit has no registered component on this host")
	}
	return selected, nil
}

func unitConfiguration(snapshot *setupconfig.Snapshot, profile string) ([]byte, error) {
	if profile == "egress-proxy" {
		return setupconfig.RenderEgressConfig(snapshot, nil)
	}
	if profile == "storage" {
		return snapshot.Manifest.StoragePolicy().Encode()
	}
	return nil, nil
}

func unitEnvironment(manifest setupconfig.Manifest, identity runtimeIdentity, hostID string, components []string) (map[string]map[string]string, error) {
	health := map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": identity.HealthAccessKey, "MOOX_HEALTH_AUTH_SECRET_KEY": identity.HealthSecret}
	environment := make(map[string]map[string]string, len(components))
	for _, id := range components {
		values := maps.Clone(health)
		environment[id] = values
		if !slices.Contains([]string{"storage-primary", "storage-node", "storage-view"}, id) {
			continue
		}
		if len(identity.StorageNodeSecret) < 32 || len(identity.StoragePrimarySecret) < 32 || len(identity.StorageViewSecret) < 32 {
			return nil, errors.New("Storage requires its original complete private core identity; missing credentials cannot be regenerated during deployment")
		}
		values["MOOX_STORAGE_HOME"] = "./var/storage"
		values["MOOX_STORAGE_ROLE"] = strings.TrimPrefix(id, "storage-")
		values["MOOX_STORAGE_CONFIG"] = "config/storage.yaml"
		values["MOOX_STORAGE_EVENTBUS_URL"] = "tls://" + net.JoinHostPort(manifest.ControlHost().Address, "4222")
		if id != "storage-view" {
			values["MOOX_STORAGE_NODE_AUTH_SECRET"] = identity.StorageNodeSecret
		}
		if id != "storage-node" {
			values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"] = identity.StoragePrimarySecret
			values["MOOX_STORAGE_VIEW_AUTH_SECRET"] = identity.StorageViewSecret
			values["MOOX_STORAGE_PRIMARY_TARGET"] = "ip://127.0.0.1:20102"
			values["MOOX_STORAGE_VIEW_TARGET"] = "ip://127.0.0.1:20103"
		}
		// The prebuilt Storage templates bind internal RPC to loopback. Until
		// split-role routing is supplied, never report readiness for an
		// installation whose peers cannot reach one another.
		primary := manifest.PlacementHost("storage-primary")
		if primary == "" || manifest.HostByID(primary).Name == "" || primary != manifest.PlacementHost("storage-node") || primary != manifest.PlacementHost("storage-view") {
			return nil, errors.New("native Storage requires primary, node and view on one host; split-role RPC routing is not configured")
		}
		if id == "storage-node" {
			values["MOOX_STORAGE_NODE_ID"] = hostID + "-storage-node"
		}
	}
	return environment, nil
}

func prepareUnitSoftware(ctx context.Context, root, state *os.Root, options CoreOptions, arch, profile string, core coreSoftware) (unitpackage.Result, string, error) {
	if profile == "host" {
		return prepareHostSoftware(ctx, root, state, options, arch, core)
	}
	work, err := os.MkdirTemp(state.Name(), "software-")
	if err != nil {
		return unitpackage.Result{}, "", err
	}
	bin := options.BinaryDirectory
	if bin == "" {
		if profile == "storage" {
			return unitpackage.Result{}, "", errors.New("storage deployment requires prebuilt Linux artifacts from setup build-linux in --binary-dir")
		}
		bin = filepath.Join(work, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			return unitpackage.Result{}, "", err
		}
		if err := buildLocalTargets(ctx, options.RepositoryRoot, bin, arch, options.Log, []string{profile}); err != nil {
			return unitpackage.Result{}, "", err
		}
	}
	software, err := unitpackage.Package(ctx, unitpackage.Options{RepositoryRoot: options.RepositoryRoot, BinaryDirectory: bin, Output: filepath.Join(work, profile+".tar.gz"), Profile: profile, GOOS: "linux", GOARCH: arch})
	if err != nil {
		return software, "", err
	}
	if core.Architecture != arch {
		hostID := filepath.Base(filepath.Dir(state.Name()))
		var operation unitOperation
		if err := readJSON(root, "hosts/"+hostID+"/operation.json", 2<<20, &operation); err != nil {
			return software, "", errors.New("business deployment requires the target host's original software state")
		}
		verified, err := unitpackage.Inspect(ctx, operation.Package.Archive)
		if err != nil || operation.Profile != "host" || operation.Source.Target.HostID != hostID || !insideState(root.Name(), operation.Package.Archive) || verified.SHA256 != operation.Package.SHA256 || verified.Manifest.Profile != "host" || verified.Manifest.GOARCH != arch || !insideState(root.Name(), operation.Helper) {
			return software, "", errors.New("target host runtime state is invalid")
		}
		digest, err := fileSHA(operation.Helper)
		if err != nil || digest != operation.HelperSHA256 || !slices.ContainsFunc(verified.Manifest.Files, func(file unitpackage.File) bool {
			return file.Path == "bin/moox-runtime" && file.SHA256 == "sha256:"+digest
		}) {
			return software, "", errors.New("target host runtime helper changed")
		}
		return software, operation.Helper, nil
	}
	return software, core.Helper, nil
}
