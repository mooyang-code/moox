package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppConfig_DefaultConfig_ShouldReturnValidDefaults(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "./data/admin.db", cfg.Database.Path)
	assert.Equal(t, 10, cfg.Database.MaxIdleConns)
	assert.Equal(t, 100, cfg.Database.MaxOpenConns)
}

func TestAppConfig_Validate_EmptyPath_ShouldReturnError(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Database.Path = ""
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "database path is required")
}

func TestAppConfig_Validate_ValidPath_ShouldCreateDirectory(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Database.Path = filepath.Join(dir, "nested", "admin.db")

	err := cfg.Validate()
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Dir(cfg.Database.Path))
	assert.NoError(t, statErr)
}

func TestAppConfig_Load_MissingFile_ShouldUseDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.NoError(t, err)
	assert.Equal(t, DefaultConfig().Database.Path, cfg.Database.Path)
}

func TestAppConfig_Load_ValidYAML_ShouldOverrideDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin.yaml")
	content := "database:\n  path: " + filepath.Join(dir, "custom.db") + "\n  max_idle_conns: 3\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "custom.db"), cfg.Database.Path)
	assert.Equal(t, 3, cfg.Database.MaxIdleConns)
}

func TestAppConfig_Load_InvalidYAML_ShouldReturnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database: ["), 0o644))

	_, err := Load(path)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestAppConfig_applyEnv_DBPath_ShouldOverridePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOOX_ADMIN_DB_PATH", filepath.Join(dir, "env.db"))

	cfg := DefaultConfig()
	cfg.applyEnv()
	assert.Equal(t, filepath.Join(dir, "env.db"), cfg.Database.Path)
}
