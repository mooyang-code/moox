//go:build linux

package unitinstall

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func sealWithHelper(t *testing.T, options SealStateOptions) (StateSeedReference, error) {
	t.Helper()
	raw, err := json.Marshal(options)
	require.NoError(t, err)
	request := filepath.Join(privateParent(t), "seal.json")
	require.NoError(t, os.WriteFile(request, append(raw, '\n'), 0o600))
	output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "seal-state", "--request", request).CombinedOutput()
	if err != nil {
		return StateSeedReference{}, err
	}
	var reference StateSeedReference
	require.NoError(t, json.Unmarshal(output, &reference))
	return reference, nil
}

func TestUnitActivationLinuxImportsActualOfflineAdminAndKeepsRollbackSnapshot(t *testing.T) {
	options := controlActivationOptions(t)
	options.Components = []string{"admin", "web-host"}
	options.Environment["admin"] = healthEnvironment()
	tools, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	adminCLI := filepath.Join(tools.Directory, "bin/moox-admin-cli")
	seedRoot := filepath.Join(options.UnitRoot, "state-imports")
	require.NoError(t, os.Mkdir(seedRoot, 0o700))
	firstSeed := filepath.Join(seedRoot, "initial")
	require.NoError(t, os.MkdirAll(filepath.Join(firstSeed, "admin/data"), 0o700))
	// Normalize only the public synthetic topology. No operator moox.toml is
	// read, copied or transported, and master/CA private files stay outside seed.
	raw, err := os.ReadFile(os.Getenv("MOOX_HOST_MATERIAL_FIXTURE"))
	require.NoError(t, err)
	var issued []struct {
		Options unitbundle.Options `json:"options"`
	}
	require.NoError(t, json.Unmarshal(raw, &issued))
	hosts := []map[string]any{}
	for _, item := range issued {
		if !item.Options.AllowOperator {
			hosts = append(hosts, map[string]any{"host_id": item.Options.HostID, "address": item.Options.Address, "private_address": item.Options.PrivateAddress, "region": "", "description": "initial", "components": item.Options.Components})
		}
	}
	require.Len(t, hosts, 3)
	trust := privateParent(t)
	master, pki := filepath.Join(trust, "admin-master.key"), filepath.Join(trust, "pki")
	bootstrap := func(directory, description string) hostbundle.Metadata {
		t.Helper()
		for _, host := range hosts {
			host["description"] = description
		}
		topology, err := json.Marshal(map[string]any{"version": 1, "control_host_id": "control", "hosts": hosts})
		require.NoError(t, err)
		topologyPath := filepath.Join(trust, "topology.json")
		require.NoError(t, os.WriteFile(topologyPath, topology, 0o600))
		output, err := exec.CommandContext(t.Context(), adminCLI, "bootstrap", "--topology-file", topologyPath, "--db-path", filepath.Join(directory, "admin/data/admin.db"), "--encryption-key-file", master, "--pki-dir", pki, "--output-dir", filepath.Join(privateParent(t), "bundles")).CombinedOutput()
		require.NoError(t, err, "real offline Admin bootstrap must succeed")
		require.NotContains(t, string(output), "PRIVATE KEY")
		var metadata hostbundle.Metadata
		require.NoError(t, json.Unmarshal(output, &metadata))
		return metadata
	}
	material := bootstrap(firstSeed, "initial")
	options.MaterialDirectory = material.BundleDir
	options.MaterialOptions.AllowOperator = true
	options.MaterialOptions.ExpectedCA = material.CA.SHA256
	options.MaterialOptions.ExpectedHash = material.ExpectedHash
	options.Environment["admin"]["MOOX_ADMIN_NODE_ID"] = "control"
	options.Environment["admin"]["MOOX_ADMIN_ENCRYPTION_KEY_FILE"] = master
	options.Environment["admin"]["MOOX_ADMIN_PKI_DIR"] = pki
	// Replace the unstarted host unit with identities issued by this actual
	// bootstrap. Both business material and host TLS now use the same root.
	host := options
	host.Profile, host.UnitRoot, host.HostUnitRoot = "host", options.HostUnitRoot, options.HostUnitRoot
	host.Archive, host.SHA256 = os.Getenv("MOOX_UNIT_INSTALL_HOST_ARCHIVE"), os.Getenv("MOOX_UNIT_INSTALL_HOST_SHA256")
	host.ReleaseID, host.Components = "seeded-host", []string{"host-gateway", "host-agent"}
	host.Environment = map[string]map[string]string{"host-gateway": healthEnvironment(), "host-agent": healthEnvironment()}
	eventbus := filepath.Join(trust, "eventbus.yaml")
	require.NoError(t, os.WriteFile(eventbus, []byte("version: 1\nurls: [nats://127.0.0.1:4222]\nusername: host-agent\neventbus_token: synthetic-token\nca_file: ''\n"), 0o600))
	host.Overrides = map[string]string{"host-agent/config/eventbus.yaml": eventbus}
	hostRelease, err := prepareWithHelper(t, host)
	require.NoError(t, err)
	_, err = activationCommand(t, "activate", "--directory", hostRelease.Directory, "--no-start")
	require.NoError(t, err)
	options.ReleaseID = "seeded-control"
	first, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	cleanupActivation(t, first)
	sealOptions := SealStateOptions{Directory: firstSeed, ReleaseDirectory: first.Directory, Paths: []string{"admin/data"}}
	reference, err := sealWithHelper(t, sealOptions)
	require.NoError(t, err)
	repeated, err := sealWithHelper(t, sealOptions)
	require.NoError(t, err)
	require.Equal(t, reference, repeated)
	_, err = activationCommand(t, "activate", "--directory", first.Directory, "--components", "web-host", "--state-seed", reference.Directory, "--state-seed-sha256", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.Error(t, err)
	_, err = os.Lstat(filepath.Join(options.UnitRoot, "activation.json"))
	require.True(t, os.IsNotExist(err), "bad input must fail before an activation journal is written")
	active, err := activationCommand(t, "activate", "--directory", first.Directory, "--components", "web-host", "--state-seed", reference.Directory, "--state-seed-sha256", reference.SHA256)
	require.NoError(t, err)
	require.Equal(t, reference.SHA256, active.StateSeedSHA256)
	firstDB := filepath.Join(first.Directory, "admin/data/admin.db")
	sourceInfo, err := os.Stat(filepath.Join(firstSeed, "admin/data/admin.db"))
	require.NoError(t, err)
	destinationInfo, err := os.Stat(firstDB)
	require.NoError(t, err)
	require.False(t, os.SameFile(sourceInfo, destinationInfo))
	readDescription := func(filename string) string {
		t.Helper()
		db, err := gorm.Open(sqlite.Open(filename), &gorm.Config{})
		require.NoError(t, err)
		connection, err := db.DB()
		require.NoError(t, err)
		defer connection.Close()
		var description string
		require.NoError(t, db.Raw("SELECT c_description FROM t_hosts WHERE c_host_id = ?", "control").Scan(&description).Error)
		return description
	}
	require.Equal(t, "initial", readDescription(firstDB))
	_, err = ReadInstalled(t.Context(), first.Directory)
	require.NoError(t, err)
	_, err = ReadPrepared(t.Context(), first.Directory)
	require.Error(t, err)
	firstPID := runScript(t, first.Directory, "status", "--components", "web-host").Components[0].PID
	require.NoError(t, os.RemoveAll(firstSeed))
	_, err = activationCommand(t, "activate", "--directory", first.Directory, "--components", "web-host", "--state-seed", reference.Directory, "--state-seed-sha256", reference.SHA256)
	require.NoError(t, err, "completed activation does not depend on keeping the consumed import source")
	require.Equal(t, firstPID, runScript(t, first.Directory, "status", "--components", "web-host").Components[0].PID)
	// Admin has not been started in this test. Its database is closed before
	// the offline rerun; this does not claim the complete five-step bootstrap.
	secondSeed := filepath.Join(seedRoot, "rerun")
	require.NoError(t, os.Mkdir(secondSeed, 0o700))
	require.NoError(t, CopyOfflineState(t.Context(), first.Directory, secondSeed, []string{"admin/data"}))
	rerunMaterial := bootstrap(secondSeed, "rerun")
	require.Equal(t, material.CA.SHA256, rerunMaterial.CA.SHA256)
	require.Equal(t, material.Credentials, rerunMaterial.Credentials)
	options.ReleaseID, options.MaterialDirectory = "rerun-control", rerunMaterial.BundleDir
	second, err := prepareWithHelper(t, options)
	require.NoError(t, err)
	secondReference, err := sealWithHelper(t, SealStateOptions{Directory: secondSeed, ReleaseDirectory: second.Directory, PreviousDirectory: first.Directory, Paths: []string{"admin/data"}})
	require.NoError(t, err)
	seedDB := filepath.Join(secondSeed, "admin/data/admin.db")
	snapshot, err := os.ReadFile(seedDB)
	require.NoError(t, err)
	changed := append([]byte(nil), snapshot...)
	changed[len(changed)-1] ^= 1
	require.NoError(t, os.WriteFile(seedDB, changed, 0o600))
	_, err = activationCommand(t, "activate", "--directory", second.Directory, "--components", "web-host", "--state-seed", secondReference.Directory, "--state-seed-sha256", secondReference.SHA256)
	require.Error(t, err, "a same-size snapshot mutation must fail before stopping old services")
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	require.Equal(t, firstPID, runScript(t, first.Directory, "status", "--components", "web-host").Components[0].PID)
	require.NoError(t, os.WriteFile(seedDB, snapshot, 0o600))
	_, err = activationCommand(t, "activate", "--directory", second.Directory, "--components", "web-host", "--state-seed", secondReference.Directory, "--state-seed-sha256", secondReference.SHA256)
	require.NoError(t, err)
	require.Equal(t, "rerun", readDescription(filepath.Join(second.Directory, "admin/data/admin.db")))
	require.Equal(t, "initial", readDescription(firstDB), "old release must remain the independent pre-rerun snapshot")
	_, err = activationCommand(t, "rollback", "--unit-root", options.UnitRoot)
	require.NoError(t, err)
	require.Equal(t, first.Directory, currentTarget(t, options.UnitRoot))
	require.Equal(t, "initial", readDescription(firstDB))
	require.Equal(t, "rerun", readDescription(filepath.Join(second.Directory, "admin/data/admin.db")))
	for _, name := range []string{"admin-master.key", "pki", "operator"} {
		_, err := os.Lstat(filepath.Join(first.Directory, name))
		require.True(t, os.IsNotExist(err))
	}
}
