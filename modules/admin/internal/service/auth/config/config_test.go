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
  access_expired: 24h
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
	assert.Equal(t, 24*time.Hour, cfg.JWT.AccessExpired)
	assert.Equal(t, 3, cfg.Security.MaxLoginAttempt)
	assert.Equal(t, 24*time.Hour, cfg.Security.SessionTTL)
	assert.Equal(t, time.Minute, cfg.Security.RequestClockSkew)
	assert.Equal(t, 2*time.Minute, cfg.Security.NonceTTL)
	assert.Equal(t, time.Minute, cfg.Security.RawTicketTTL)
}

func TestConfig_LoadConfig_RejectsInvalidSecurityDurations(t *testing.T) {
	tests := []struct{ name, field string }{
		{name: "session ttl", field: "session_ttl: 0s"},
		{name: "clock skew", field: "request_clock_skew: 0s"},
		{name: "nonce ttl", field: "nonce_ttl: 0s"},
		{name: "raw ticket ttl", field: "raw_ticket_ttl: 0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
			content := "jwt:\n  access_expired: 24h\nsecurity:\n  session_ttl: 24h\n  request_clock_skew: 60s\n  nonce_ttl: 2m\n  raw_ticket_ttl: 60s\n  " + tt.field + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "gateway.yaml"), []byte(content), 0o644))
			origWD, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(dir))
			t.Cleanup(func() { _ = os.Chdir(origWD) })
			_, err = LoadConfig()
			require.Error(t, err)
		})
	}
}

func TestConfig_LoadConfig_RequiresAccessExpiryToMatchSessionTTL(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	content := "jwt:\n  access_expired: 1h\nsecurity:\n  session_ttl: 24h\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "gateway.yaml"), []byte(content), 0o644))
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	_, err = LoadConfig()
	require.Error(t, err)
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
