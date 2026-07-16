package config

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigIncludesSyncDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Sync.Enabled {
		t.Fatalf("sync must be enabled by default")
	}
	if cfg.Sync.WindowHours != 24 {
		t.Fatalf("WindowHours=%d, want 24", cfg.Sync.WindowHours)
	}
	if cfg.Sync.PageSize != 500 {
		t.Fatalf("PageSize=%d, want 500", cfg.Sync.PageSize)
	}
	if cfg.Sync.MaxSymbolsPerRun != 10 {
		t.Fatalf("MaxSymbolsPerRun=%d, want 10", cfg.Sync.MaxSymbolsPerRun)
	}
	if cfg.Health.Addr != ":11210" {
		t.Fatalf("Health.Addr=%q, want :11210", cfg.Health.Addr)
	}
}

func TestLoadAppliesHealthAddrFromEnv(t *testing.T) {
	t.Setenv("MOOX_TRADE_HEALTH_ADDR", "127.0.0.1:16210")

	cfg := DefaultConfig()
	cfg.applyEnv()
	if cfg.Health.Addr != "127.0.0.1:16210" {
		t.Fatalf("Health.Addr=%q, want 127.0.0.1:16210", cfg.Health.Addr)
	}
}

func TestLoad_FromValidYAML_ShouldApplyAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
database:
  type: sqlite
  path: `+filepath.Join(dir, "data", "trade.db")+`
health:
  addr: ":19999"
eventbus:
  enabled: false
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ":19999", cfg.Health.Addr)
	assert.Equal(t, filepath.Join(dir, "data", "trade.db"), cfg.Database.Path)
}

func TestLoad_MissingFile_ShouldUseDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.NoError(t, err)
	assert.Equal(t, ":11210", cfg.Health.Addr)
}

func TestValidate_EventBusEnabledWithoutURLs_ShouldFail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.URLs = nil
	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "eventbus urls are required")
}

func TestValidate_EventBusDisabled_ShouldPass(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.Enabled = false
	require.NoError(t, cfg.Validate())
}

func TestSetAndGetGlobalConfig_ShouldRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Health.Addr = ":17777"
	SetGlobalConfig(cfg)
	got := GetGlobalConfig()
	assert.Equal(t, ":17777", got.Health.Addr)
}

func TestValidate_NormalizesSyncDefaults(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.Enabled = false
	cfg.Sync.WindowHours = 0
	cfg.Sync.PageSize = 0
	cfg.Sync.MaxSymbolsPerRun = 0
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 24, cfg.Sync.WindowHours)
	assert.Equal(t, 500, cfg.Sync.PageSize)
	assert.Equal(t, 10, cfg.Sync.MaxSymbolsPerRun)
}

func TestApplyEnv_OverridesBusinessFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "env.db")
	t.Setenv("MOOX_TRADE_DB_PATH", dbPath)
	t.Setenv("MOOX_TRADE_ENCRYPTION_KEY", "env-secret")
	t.Setenv("MOOX_TRADE_CONTROL_GATEWAY", "http://127.0.0.1:18080")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "env-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "env-sk")
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")

	cfg := DefaultConfig()
	cfg.applyEnv()

	assert.Equal(t, dbPath, cfg.Database.Path)
	assert.Equal(t, "env-secret", cfg.Security.EncryptionKey)
	assert.Equal(t, "http://127.0.0.1:18080", cfg.ControlGateway.BaseURL)
	assert.Equal(t, "env-ak", cfg.ControlGateway.ServiceAuth.AccessKey)
	assert.Equal(t, "env-sk", cfg.ControlGateway.ServiceAuth.SecretKey)
	assert.Equal(t, "gateway-gz-122", cfg.ControlGateway.ServiceAuth.TargetNode)
}

func TestLoad_InvalidYAML_ShouldReturnParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database: ["), 0o644))

	_, err := Load(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestValidate_SQLiteWithoutPath_ShouldFail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.Enabled = false
	cfg.Database.Path = ""

	err := cfg.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database path is required")
}

func TestValidate_EventBusEnabledWithoutDurables_ShouldFail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.ExecutionDurable = ""

	err := cfg.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "eventbus stream and durable are required")
}
