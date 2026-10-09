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
	assert.Equal(t, "trade", cfg.GatewayClient.Caller)
	assert.Equal(t, "../secrets/caller-trade.key", cfg.GatewayClient.KeyFile)
	require.NoError(t, cfg.GatewayClient.Validate())
	assert.True(t, cfg.EventBus.Enabled)
}

func TestLoadRejectsRemovedDNSResolverSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("dns_resolver:\n  enabled: false\n"), 0o644))
	_, err := Load(path)
	require.Error(t, err, "DNS 解析已移到出口代理，交易服务不再接受 dns_resolver 段")
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

	cfg := DefaultConfig()
	require.NoError(t, cfg.applyEnv())

	assert.Equal(t, dbPath, cfg.Database.Path)
	assert.True(t, cfg.Runtime.LiveTradingEnabled)
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
