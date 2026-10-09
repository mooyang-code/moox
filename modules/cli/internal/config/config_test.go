package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_getConfigPaths_WithEnvOverride_ShouldPreferEnvPath(t *testing.T) {
	t.Setenv("MOOX_CONFIG", "/tmp/custom-cli.yaml")
	paths := getConfigPaths()
	require.NotEmpty(t, paths)
	assert.Equal(t, "/tmp/custom-cli.yaml", paths[0])
}

func TestConfig_LoadConfig_ValidYAML_ShouldParseFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli.yaml")
	content := `doctor:
  node_id: fixture-node
  release_root: /isolated/release
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "fixture-node", cfg.Doctor.NodeID)
	assert.Equal(t, "/isolated/release", cfg.Doctor.ReleaseRoot)
}

func TestConfig_LoadConfig_MissingFile_ShouldReturnError(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	_, err = LoadConfig()
	assert.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestEffectiveDoctorUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("MOOX_NODE_ID", "node-a")
	t.Setenv("MOOX_DOCTOR_MONITOR_TARGET", "ip://monitor:11410")
	got := (&Config{}).EffectiveDoctor()
	assert.Equal(t, "node-a", got.NodeID)
	assert.Equal(t, "config/setup/service-deployments.yaml", got.SeedPath)
	assert.Equal(t, "config/setup/dataset-health-policy.yaml", got.DatasetHealthPolicyPath)
}

func TestCLIConfigRejectsRemovedRPCFields(t *testing.T) {
	for _, raw := range []string{
		"doctor:\n  monitor_target: ip://127.0.0.1:11410\n",
		"storage:\n  target: ip://127.0.0.1:20102\n",
		"moox:\n  auth_target: ip://127.0.0.1:11100\n",
		"doctor:\n  node_id: first\n  node_id: second\n",
		"doctor: {}\n---\ndoctor: {}\n",
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cli.yaml")
			require.NoError(t, os.WriteFile(path, []byte(raw), 0600))
			t.Setenv("MOOX_CONFIG", path)
			_, err := LoadConfig()
			require.Error(t, err)
		})
	}
}
