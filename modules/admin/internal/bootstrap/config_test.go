package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/server"
)

func TestLoadEncryptionKey_FromEnv_ShouldReturnKey(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "")
	key, err := loadEncryptionKey()
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", key)
}

func TestLoadEncryptionKey_MissingFileEnv_ShouldError(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "")
	_, err := loadEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_ADMIN_ENCRYPTION_KEY_FILE is required")
}

func TestLoadEncryptionKey_InvalidFileMode_ShouldError(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "bad-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("secret-key-value-32bytes-long!!"), 0o644))
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", keyPath)

	_, err := loadEncryptionKey()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regular 0600 file")
}

func TestLoadEncryptionKey_ValidFile_ShouldReturnKeyWithoutMutatingEnv(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	keyPath := filepath.Join(t.TempDir(), "good-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcdef"), 0o600))
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", keyPath)

	key, err := loadEncryptionKey()
	require.NoError(t, err)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", key)
	assert.Empty(t, os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY"), "主密钥不得回写进进程环境变量")
}

func setupBootstrapConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	require.NoError(t, os.MkdirAll(configDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(configDir, "app.yaml"), []byte(`database:
  path: ./data/admin.db
session:
  ttl: 12h
console:
  allowed_origins:
    - "http://localhost:9527"
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "trpc_go.yaml"), []byte(`server:
  service:
    - name: trpc.moox.admin.GatewayControl
      ip: 127.0.0.1
      port: 11112
      network: tcp
      protocol: trpc
`), 0o644))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("MOOX_ADMIN_NODE_ID", "admin-node-test")
	t.Setenv("MOOX_ADMIN_WEB_ADDRESS", "203.0.113.10")
	t.Setenv("MOOX_ADMIN_WEB_LISTEN", "")
	t.Setenv("MOOX_ADMIN_PKI_DIR", "/fixture/secrets/pki")
	return dir
}

func TestLoadConfigs_ValidFiles_ShouldLoadAllModules(t *testing.T) {
	setupBootstrapConfigDir(t)
	cfg, err := loadConfigsForTest(t)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.NotNil(t, cfg.App)
	assert.NotNil(t, cfg.Auth)
	assert.NotNil(t, cfg.Console)
	assert.Equal(t, "admin-node-test", cfg.AdminNodeID)
	assert.Equal(t, "0123456789abcdef0123456789abcdef", cfg.EncryptionKey)
	assert.Equal(t, 12*time.Hour, cfg.Auth.Session.TTL)
	assert.Equal(t, []string{"http://localhost:9527"}, cfg.Console.Console.AllowedOrigins)
	assert.Equal(t, "0.0.0.0:9527", cfg.Web.Listen, "浏览器入口默认监听 9527")
	assert.Equal(t, "203.0.113.10", cfg.Web.Address)
	assert.Equal(t, "/fixture/secrets/pki", cfg.Web.PKIDir, "网页证书用 MooX CA 签")
	assert.Equal(t, filepath.Join(filepath.Dir(cfg.App.Database.Path), "web-cert"), cfg.Web.CertDir, "网页证书放在 Admin 数据目录")
}

func TestLoadConfigs_WebEntryRequiresControlAddress(t *testing.T) {
	for _, address := range []string{"", "203.0.113.10:9527", "https://203.0.113.10"} {
		setupBootstrapConfigDir(t)
		t.Setenv("MOOX_ADMIN_WEB_ADDRESS", address)
		_, err := loadConfigsForTest(t)
		require.Error(t, err, address)
		assert.Contains(t, err.Error(), "MOOX_ADMIN_WEB_ADDRESS")
	}
	setupBootstrapConfigDir(t)
	t.Setenv("MOOX_ADMIN_WEB_LISTEN", "127.0.0.1:19527")
	cfg, err := loadConfigsForTest(t)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:19527", cfg.Web.Listen)
}

func TestLoadConfigs_MissingAdminNodeID_ShouldFailAtStartup(t *testing.T) {
	setupBootstrapConfigDir(t)
	t.Setenv("MOOX_ADMIN_NODE_ID", "")

	_, err := loadConfigsForTest(t)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_ADMIN_NODE_ID")
}

func TestLoadConfigs_RejectsWildcardDevelopmentOrigin(t *testing.T) {
	dir := setupBootstrapConfigDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "app.yaml"), []byte("database:\n  path: ./data/admin.db\nconsole:\n  allowed_origins:\n    - \"*\"\n"), 0o644))
	_, err := loadConfigsForTest(t)
	require.ErrorContains(t, err, "console.allowed_origins")
}

func TestRegisterMetricsReporter_MissingService_ShouldSkip(t *testing.T) {
	require.NotPanics(t, func() {
		registerMetricsReporter(&server.Server{})
	})
}

func TestGatewayControlListenerRequiresOneDedicatedLoopbackEndpoint(t *testing.T) {
	base := "    - name: trpc.moox.admin.GatewayControl\n      ip: 127.0.0.1\n      port: 11112\n      network: tcp\n      protocol: trpc\n"
	for _, tc := range []struct {
		name, entry string
		valid       bool
	}{
		{"loopback", base, true},
		{"ipv6 loopback", strings.Replace(base, "127.0.0.1", "::1", 1), true},
		{"public", strings.Replace(base, "127.0.0.1", "0.0.0.0", 1), false},
		{"hostname", strings.Replace(base, "127.0.0.1", "localhost", 1), false},
		{"wrong port", strings.Replace(base, "11112", "11003", 1), false},
		{"http", strings.Replace(base, "protocol: trpc", "protocol: http", 1), false},
		{"udp", strings.Replace(base, "network: tcp", "network: udp", 1), false},
		{"address override", base + "      address: 0.0.0.0:11112\n", false},
		{"NIC override", base + "      nic: eth0\n", false},
		{"duplicate", base + base, false},
		{"missing", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trpc_go.yaml")
			require.NoError(t, os.WriteFile(path, []byte("server:\n  service:\n"+tc.entry), 0o600))
			err := validateGatewayControlListener(path)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

// loadConfigsForTest 让 tRPC 框架配置路径指向测试目录里的 config/trpc_go.yaml（生产由 -conf 指定）。
func loadConfigsForTest(t *testing.T) (*Config, error) {
	t.Helper()
	previous := trpc.ServerConfigPath
	trpc.ServerConfigPath = "./config/trpc_go.yaml"
	t.Cleanup(func() { trpc.ServerConfigPath = previous })
	return LoadConfigs(context.Background(), "./config/app.yaml")
}
