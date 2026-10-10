package unitdeploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

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
