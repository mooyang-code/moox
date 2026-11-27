package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppConfig_DefaultConfig_ShouldReturnValidDefaults(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "./data/admin.db", cfg.Database.Path)
	assert.Equal(t, 4, cfg.Database.MaxIdleConns)
	assert.Equal(t, 8, cfg.Database.MaxOpenConns)
	assert.Equal(t, 24*time.Hour, cfg.Session.TTL)
	assert.Equal(t, 5, cfg.Security.MaxLoginAttempt)
	assert.Equal(t, 15*time.Minute, cfg.Security.LockDuration)
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

func TestAppConfig_Load_MissingFile_ShouldError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

func TestAppConfig_Load_EmptyPath_ShouldUseDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOOX_ADMIN_DB_PATH", filepath.Join(dir, "admin.db"))
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "admin.db"), cfg.Database.Path)
	assert.Equal(t, 24*time.Hour, cfg.Session.TTL)
}

func TestAppConfig_Load_ValidYAML_ShouldOverrideDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	content := "database:\n  path: " + filepath.Join(dir, "custom.db") + "\n  max_idle_conns: 3\nsession:\n  ttl: 12h\nsecurity:\n  max_login_attempt: 7\n  lock_duration: 20m\nconsole:\n  allowed_origins:\n    - \"http://localhost:9527\"\nrate_limit:\n  method_limits:\n    \"/api/admin/auth/Login\":\n      qps: 1\n      burst: 2\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "custom.db"), cfg.Database.Path)
	assert.Equal(t, 3, cfg.Database.MaxIdleConns)
	assert.Equal(t, 12*time.Hour, cfg.Session.TTL)
	assert.Equal(t, 7, cfg.Security.MaxLoginAttempt)
	assert.Equal(t, 20*time.Minute, cfg.Security.LockDuration)
	assert.Equal(t, []string{"http://localhost:9527"}, cfg.Console.AllowedOrigins)
	assert.Equal(t, 2, cfg.RateLimit.MethodLimits["/api/admin/auth/Login"].Burst)
}

func TestAppConfig_Load_UnknownField_ShouldError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  path: "+filepath.Join(dir, "a.db")+"\nunknown_section: true\n"), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestAppConfig_Load_InvalidOriginsAndBounds_ShouldError(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "a.db")
	for _, origin := range []string{"*", "https://*.example.com", "localhost:9527", "https://a.example/path", "ftp://a.example"} {
		path := filepath.Join(dir, "origin.yaml")
		require.NoError(t, os.WriteFile(path, []byte("database:\n  path: "+db+"\nconsole:\n  allowed_origins:\n    - \""+origin+"\"\n"), 0o644))
		_, err := Load(path)
		require.ErrorContains(t, err, "console.allowed_origins", origin)
	}
	path := filepath.Join(dir, "bad-session.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database:\n  path: "+db+"\nsession:\n  ttl: 0s\n"), 0o644))
	_, err := Load(path)
	require.ErrorContains(t, err, "session.ttl")
}

func TestShippedAppConfigHasNoExtraOrigins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MOOX_ADMIN_DB_PATH", filepath.Join(dir, "admin.db"))
	cfg, err := Load(filepath.Join("..", "..", "config", "app.yaml"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Console.AllowedOrigins)
	assert.Contains(t, cfg.RateLimit.MethodLimits, "/api/admin/auth/Login")
	assert.Equal(t, 24*time.Hour, cfg.Session.TTL)
	assert.Equal(t, 5, cfg.Security.MaxLoginAttempt)
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
