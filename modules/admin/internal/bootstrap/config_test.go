package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/console"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestValidateSetupListenerRequiresLoopback(t *testing.T) {
	for _, tt := range []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "ipv4 loopback", address: "127.0.0.1"},
		{name: "ipv6 loopback", address: "::1"},
		{name: "all interfaces", address: "0.0.0.0", wantErr: true},
		{name: "public address", address: "192.0.2.10", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trpc_go.yaml")
			body := "server:\n  service:\n    - name: trpc.moox.admin.Setup\n      ip: " + tt.address + "\n      port: 11110\n      network: tcp\n      protocol: trpc\n"
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
			err := validateSetupListener(path)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "loopback")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateSetupListenerRequiresDedicatedService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trpc_go.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  service: []\n"), 0o600))
	err := validateSetupListener(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trpc.moox.admin.Setup")
}

func TestGatewayContainsWildcardOrigin_HasWildcard_ShouldReturnTrue(t *testing.T) {
	assert.True(t, gatewayContainsWildcardOrigin([]string{"https://a.com", "*"}))
}

func TestGatewayContainsWildcardOrigin_NoWildcard_ShouldReturnFalse(t *testing.T) {
	assert.False(t, gatewayContainsWildcardOrigin([]string{"https://a.com", "https://b.com"}))
}

func TestValidateGatewayCORS_NilConfig_ShouldError(t *testing.T) {
	err := validateGatewayCORS(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestValidateGatewayCORS_ProdEmptyOrigins_ShouldError(t *testing.T) {
	cfg := &console.Config{Gateway: console.GatewayConfig{Debug: false}}
	err := validateGatewayCORS(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cors.allowed_origins")
}

func TestValidateGatewayCORS_DebugEmptyOrigins_ShouldPass(t *testing.T) {
	cfg := &console.Config{Gateway: console.GatewayConfig{Debug: true}}
	require.NoError(t, validateGatewayCORS(cfg))
}

func TestValidateGatewayCORS_WithOrigins_ShouldPass(t *testing.T) {
	cfg := &console.Config{
		Gateway: console.GatewayConfig{Debug: false},
		CORS:    console.CORSConfig{AllowedOrigins: []string{"https://admin.example.com"}},
	}
	require.NoError(t, validateGatewayCORS(cfg))
}

func TestLoadEncryptionKey_FromEnv_ShouldPass(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "")
	require.NoError(t, loadEncryptionKey())
}

func TestLoadEncryptionKey_MissingFileEnv_ShouldError(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "")
	err := loadEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_ADMIN_ENCRYPTION_KEY_FILE is required")
}

func TestLoadEncryptionKey_InvalidFileMode_ShouldError(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "bad-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("secret-key-value-32bytes-long!!"), 0o644))
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", keyPath)

	err := loadEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regular 0600 file")
}

func TestLoadEncryptionKey_ValidFile_ShouldSetEnv(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "good-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0o600))
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", keyPath)

	require.NoError(t, loadEncryptionKey())
	assert.Equal(t, "0123456789abcdef0123456789abcdef", os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY"))
}

func setupBootstrapConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte(`database:
  path: ./data/admin.db
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "console.yaml"), []byte(`jwt:
  secret_key: test-secret-key-32bytes-long-123456
gateway:
  debug: true
cors:
  allowed_origins:
    - "*"
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "trpc_go.yaml"), []byte(`server:
  service:
    - name: trpc.moox.admin.Setup
      ip: 127.0.0.1
      port: 11110
      network: tcp
      protocol: trpc
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
	assert.Equal(t, "test-secret-key-32bytes-long-123456", cfg.Gateway.JWT.SecretKey)
}

func TestLoadConfigs_EmptyJWTSecret_ShouldError(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte(`database:
  path: ./data/admin.db
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "console.yaml"), []byte(`jwt:
  secret_key: ""
gateway:
  debug: true
cors:
  allowed_origins:
    - "*"
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "trpc_go.yaml"), []byte(`server:
  service:
    - name: trpc.moox.admin.Setup
      ip: 127.0.0.1
      port: 11110
      network: tcp
      protocol: trpc
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
