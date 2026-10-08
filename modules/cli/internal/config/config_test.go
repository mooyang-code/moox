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
  node_id: node-from-file
  release_root: /opt/moox
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "node-from-file", cfg.Doctor.NodeID)
	assert.Equal(t, "/opt/moox", cfg.Doctor.ReleaseRoot)
}

func TestConfig_LoadConfig_MissingFile_ShouldReturnError(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	_, err = LoadConfig()
	assert.Error(t, err)
}

func TestEffectiveDoctorUsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("MOOX_NODE_ID", "node-a")
	t.Setenv("MOOX_RELEASE_ROOT", "/opt/moox")
	got := (&Config{}).EffectiveDoctor()
	assert.Equal(t, "node-a", got.NodeID)
	assert.Equal(t, "/opt/moox", got.ReleaseRoot)
	assert.Equal(t, "config/setup/service-deployments.yaml", got.SeedPath)
	assert.Equal(t, "config/setup/dataset-health-policy.yaml", got.DatasetHealthPolicyPath)
}
