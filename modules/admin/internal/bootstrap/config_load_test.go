package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

func setupBootstrapConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte(`database:
  path: ./data/admin.db
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "gateway.yaml"), []byte(`jwt:
  secret_key: test-secret-key-32bytes-long!!
gateway:
  debug: true
cors:
  allowed_origins:
    - "*"
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "dnsproxy.yaml"), []byte(`dns:
  local_resolve_enabled: false
`), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	return dir
}

func TestLoadConfigs_ValidFiles_ShouldLoadAllModules(t *testing.T) {
	setupBootstrapConfigDir(t)
	cfg, err := LoadConfigs(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.NotNil(t, cfg.App)
	assert.NotNil(t, cfg.Auth)
	assert.NotNil(t, cfg.Gateway)
	assert.Equal(t, "test-secret-key-32bytes-long!!", cfg.Gateway.JWT.SecretKey)
}

func TestLoadConfigs_EmptyJWTSecret_ShouldError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte(`database:
  path: ./data/admin.db
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "gateway.yaml"), []byte(`jwt:
  secret_key: ""
gateway:
  debug: true
cors:
  allowed_origins:
    - "*"
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "dnsproxy.yaml"), []byte(`dns:
  local_resolve_enabled: false
`), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")

	_, err = LoadConfigs(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "jwt.secret_key")
}

func TestRegisterMetricsReporter_MissingService_ShouldSkip(t *testing.T) {
	require.NotPanics(t, func() {
		registerMetricsReporter(&server.Server{})
	})
}
