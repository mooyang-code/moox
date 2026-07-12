package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_LoadConfig_ValidYAML_ShouldParseFields(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	path := filepath.Join(configDir, "gateway.yaml")
	content := `cache:
  data_dir: /tmp/auth-cache
jwt:
  secret_key: test-secret
  access_expired: 30m
security:
  salt_expired: 5m
  max_login_attempt: 3
  lock_duration: 10m
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/auth-cache", cfg.Cache.DataDir)
	assert.Equal(t, "test-secret", cfg.JWT.SecretKey)
	assert.Equal(t, 30*time.Minute, cfg.JWT.AccessExpired)
	assert.Equal(t, 3, cfg.Security.MaxLoginAttempt)
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

func TestConfig_LoadConfig_InvalidYAML_ShouldReturnError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	path := filepath.Join(configDir, "gateway.yaml")
	require.NoError(t, os.WriteFile(path, []byte("cache: ["), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	_, err = LoadConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "解析YAML失败")
}
