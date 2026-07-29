package config

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigContainsOnlyRuntimeInputs(t *testing.T) {
	cfg := DefaultConfig()
	assert.False(t, cfg.Runtime.LiveTradingEnabled)
	assert.Equal(t, "./data/moox_trade.db", cfg.Database.Path)
	assert.Equal(t, "https://106.53.107.122:11001", cfg.Admin.BaseURL)
	assert.True(t, cfg.EventBus.Enabled)
}

func TestLoad_FromValidYAML_ShouldApplyAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
database:
  path: `+filepath.Join(dir, "data", "trade.db")+`
eventbus:
  enabled: false
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "data", "trade.db"), cfg.Database.Path)
}

func TestLoad_MissingFile_ShouldUseDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "./data/moox_trade.db", cfg.Database.Path)
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

func TestApplyEnv_OverridesBusinessFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "env.db")
	t.Setenv("MOOX_TRADE_DB_PATH", dbPath)
	t.Setenv("MOOX_TRADE_LIVE_TRADING_ENABLED", "true")
	t.Setenv("MOOX_TRADE_ADMIN_URL", "http://127.0.0.1:18080")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "env-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "env-sk")
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")

	cfg := DefaultConfig()
	require.NoError(t, cfg.applyEnv())

	assert.Equal(t, dbPath, cfg.Database.Path)
	assert.True(t, cfg.Runtime.LiveTradingEnabled)
	assert.Equal(t, "http://127.0.0.1:18080", cfg.Admin.BaseURL)
	assert.Equal(t, "env-ak", cfg.Admin.ServiceAuth.AccessKey)
	assert.Equal(t, "env-sk", cfg.Admin.ServiceAuth.SecretKey)
	assert.Equal(t, "gateway-gz-122", cfg.Admin.ServiceAuth.TargetNode)
}

func TestLoad_InvalidYAML_ShouldReturnParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("database: ["), 0o644))

	_, err := Load(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse config file")
}

func TestLoad_UnknownLegacyField_ShouldFail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	require.NoError(t, os.WriteFile(path, []byte("sync:\n  enabled: true\n"), 0o644))

	_, err := Load(path)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "field sync not found")
}

func TestLoadRejectsInvalidLiveTradingEnvironment(t *testing.T) {
	t.Setenv("MOOX_TRADE_LIVE_TRADING_ENABLED", "sometimes")
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOOX_TRADE_LIVE_TRADING_ENABLED")
}

func TestValidate_WithoutDatabasePath_ShouldFail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.Enabled = false
	cfg.Database.Path = ""

	err := cfg.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database path is required")
}

func TestValidate_EventBusEnabledWithoutConsumer_ShouldFail(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EventBus.TargetConsumer = ""

	err := cfg.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "eventbus target consumer is required")
}
