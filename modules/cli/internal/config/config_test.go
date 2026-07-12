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
	content := `storage:
  target: storage.local:8001
moox:
  auth_target: 127.0.0.1:9001
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg.MooX)
	assert.Equal(t, "storage.local:8001", cfg.Storage.Target)
	assert.Equal(t, "127.0.0.1:9001", cfg.MooX.AuthTarget)
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
