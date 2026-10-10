package unitdeploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbootstrap"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/stretchr/testify/require"
)

func TestCoreRequestUsesConfiguredControlRootAndPrivateAdministrator(t *testing.T) {
	manifest := setupconfig.Manifest{Admin: setupconfig.Admin{Username: "admin", Password: "synthetic-bootstrap-password"}, Paths: setupconfig.Paths{DeployRoot: "/data/moox", ControlRoot: "/data/moox/units/prod"}}
	request := coreRequest(manifest, runtimeIdentity{}, unitpackage.Result{}, unitpackage.Result{})
	require.Equal(t, manifest.Paths.ControlRoot, request.Control.UnitRoot)
	require.Equal(t, "/data/moox/host", request.Host.UnitRoot)
	require.Equal(t, manifest.Admin.Username, request.InitialAdmin.Username)
	require.Equal(t, manifest.Admin.Password, request.InitialAdmin.Password)
}

func TestUnitDeploymentRejectsReceiptForDifferentRootsBeforeSSH(t *testing.T) {
	const fixture = `[admin]
username = "admin"
password = "synthetic-password"
[tencent_cloud]
secret_id = "synthetic-id"
secret_key = "synthetic-key"
[eventbus]
port = 4222
tls_enabled = true
[hosts.control]
address = "192.0.2.10"
ssh = {username="fixture"}
[placements]
control = ["admin","eventbus","console-proxy","web-host"]
`
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "moox.toml"), []byte(fixture), 0o600))
	snapshot, err := setupconfig.Load(filepath.Join(directory, "moox.toml"), directory)
	require.NoError(t, err)
	state, err := privateRoot(filepath.Join(directory, "state"))
	require.NoError(t, err)
	defer state.Close()
	control := snapshot.Manifest.ControlHost()
	_, err = loadIdentity(state, control, snapshot.Manifest.Paths.DeployRoot)
	require.NoError(t, err)
	ready := CoreResult{Stage: "core-ready", Bootstrap: unitbootstrap.Result{HostID: control.Name, Phase: "complete", HostDirectory: filepath.Join(snapshot.Manifest.Paths.DeployRoot, "host", "releases", "initial"), ControlDirectory: filepath.Join(snapshot.Manifest.Paths.ControlRoot, "releases", "initial")}}
	for _, changed := range []string{"host", "control"} {
		invalid := ready
		if changed == "host" {
			invalid.Bootstrap.HostDirectory = filepath.Join(snapshot.Manifest.Paths.DeployRoot, "unrelated", "releases", "initial")
		} else {
			invalid.Bootstrap.ControlDirectory = filepath.Join(snapshot.Manifest.Paths.ControlRoot+"-other", "releases", "initial")
		}
		require.NoError(t, saveJSON(state, "core-ready.json", invalid, true))
		_, err := DeployUnit(t.Context(), snapshot, UnitOptions{CoreOptions: CoreOptions{StateDirectory: state.Name()}, HostID: control.Name, Profile: "host"})
		require.ErrorContains(t, err, "roots from its original core receipt")
	}
}

func TestRuntimeIdentityIsPrivatePersistentAndBoundToTarget(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(directory, 0o700))
	root, err := privateRoot(directory)
	require.NoError(t, err)
	defer root.Close()
	var missing unitOperation
	require.True(t, os.IsNotExist(readJSON(root, "operation.json", 4096, &missing)), "an absent checkpoint must be distinguishable from invalid private material")
	host := setupconfig.Host{Name: "control", Address: "192.0.2.1"}
	initial, err := loadIdentity(root, host, "/data/moox")
	require.NoError(t, err)
	repeated, err := loadIdentity(root, host, "/data/moox")
	require.NoError(t, err)
	require.Equal(t, initial, repeated)
	require.NotContains(t, initial.String(), initial.JWTSecret)
	_, err = loadIdentity(root, setupconfig.Host{Name: "other", Address: host.Address}, "/data/moox")
	require.Error(t, err)
	info, err := os.Stat(filepath.Join(directory, "runtime-identity.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, os.Chmod(filepath.Join(directory, "runtime-identity.json"), 0o644))
	_, err = loadIdentity(root, host, "/data/moox")
	require.Error(t, err)
}

func TestCoreBuildUsesOnlyLocalPureGoAndFrontendTools(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"web", "web-host", "tools"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0o700))
	}
	log := filepath.Join(root, "build.log")
	for _, tool := range []string{"npm", "make", "bash"} {
		script := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s|%s\\n' '" + tool + "' \"$CGO_ENABLED\" \"$TARGET_GOOS\" \"$TARGET_GOARCH\" \"${GOOS-unset}\" \"$*\" >> \"$MOOX_TEST_BUILD_LOG\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "tools", tool), []byte(script), 0o700))
	}
	t.Setenv("PATH", filepath.Join(root, "tools"))
	t.Setenv("MOOX_TEST_BUILD_LOG", log)
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOOS", "windows")
	t.Setenv("GOARCH", "386")
	require.NoError(t, buildCoreLocally(t.Context(), root, filepath.Join(root, "bin"), "arm64", nil))
	raw, err := os.ReadFile(log)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 14)
	for _, line := range lines {
		require.Contains(t, line, "|0|linux|arm64|unset|")
		require.NotContains(t, line, "storage")
	}
	require.Contains(t, string(raw), "npm|0|linux|arm64|unset|ci")
	require.Contains(t, string(raw), "make|0|linux|arm64|unset|statik")
	require.Contains(t, string(raw), "build.sh host-gateway")
}

func TestHostBuildUsesOnlyLocalPureGoTools(t *testing.T) {
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	require.NoError(t, os.Mkdir(tools, 0o700))
	log := filepath.Join(root, "host-build.log")
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s\\n' \"$CGO_ENABLED\" \"$TARGET_GOOS\" \"$TARGET_GOARCH\" \"${GOOS-unset}\" \"$*\" >> \"$MOOX_TEST_BUILD_LOG\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(tools, "bash"), []byte(script), 0o700))
	t.Setenv("PATH", tools)
	t.Setenv("MOOX_TEST_BUILD_LOG", log)
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOOS", "windows")
	t.Setenv("GOARCH", "386")
	require.NoError(t, buildHostLocally(t.Context(), root, filepath.Join(root, "bin"), "arm64", nil))
	raw, err := os.ReadFile(log)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	require.Len(t, lines, 2)
	for _, line := range lines {
		require.Contains(t, line, "0|linux|arm64|unset|")
	}
	require.Contains(t, lines[0], "build.sh host-gateway")
	require.Contains(t, lines[1], "build.sh hostagent")
}
