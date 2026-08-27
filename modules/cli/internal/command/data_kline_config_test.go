package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validDataAccessYAML = `version: 1
gateway:
  target: ip://127.0.0.1:11003
  target_node: storage
  key_id: moox-skill
  caller: moox-skill
  secret: gateway-secret
storage:
  app_id: moox-skill
  app_key: storage-key
data_types:
  crypto:
    default_exchange: binance
    exchanges:
      binance:
        space_id: crypto_market
        series_tag: venue:binance
        kline_datasets:
          1m: binance_spot_kline_1m
`

func writeDataAccessConfig(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data-access.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
	return path
}

func TestDataAccessConfigPathPrecedence(t *testing.T) {
	t.Setenv(dataAccessConfigEnv, "/env/config.yaml")
	assert.Equal(t, "/explicit/config.yaml", resolveDataAccessConfigPath(" /explicit/config.yaml "))
	assert.Equal(t, "/env/config.yaml", resolveDataAccessConfigPath(""))
	t.Setenv(dataAccessConfigEnv, "")
	assert.Equal(t, defaultDataAccessConfigPath, resolveDataAccessConfigPath(""))
}

func TestDataAccessConfigLoadsStrictCatalog(t *testing.T) {
	path := writeDataAccessConfig(t, validDataAccessYAML, 0o600)
	cfg, err := loadDataAccessConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "ip://127.0.0.1:11003", cfg.Gateway.Target)
	selection, err := cfg.resolveKline(" CRYPTO ", "", " 1M ")
	require.NoError(t, err)
	assert.Equal(t, "binance", selection.Exchange)
	assert.Equal(t, "crypto_market", selection.SpaceID)
	assert.Equal(t, "binance_spot_kline_1m", selection.DatasetID)
	assert.Equal(t, "venue:binance", selection.SeriesTag)
}

func TestDataAccessConfigRejectsUnknownFieldAndVersion(t *testing.T) {
	unknown := writeDataAccessConfig(t, validDataAccessYAML+"unexpected: true\n", 0o600)
	_, err := loadDataAccessConfig(unknown)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown")

	badVersion := writeDataAccessConfig(t, "version: 2\n"+validDataAccessYAML[len("version: 1\n"):], 0o600)
	_, err = loadDataAccessConfig(badVersion)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "version")
}

func TestDataAccessConfigRejectsUnsafeFiles(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		path := writeDataAccessConfig(t, validDataAccessYAML, 0o644)
		_, err := loadDataAccessConfig(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0600")
	})

	t.Run("symlink", func(t *testing.T) {
		target := writeDataAccessConfig(t, validDataAccessYAML, 0o600)
		link := filepath.Join(t.TempDir(), "config-link.yaml")
		require.NoError(t, os.Symlink(target, link))
		_, err := loadDataAccessConfig(link)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink")
	})

	t.Run("not regular", func(t *testing.T) {
		_, err := loadDataAccessConfig(t.TempDir())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "regular")
	})
}

func TestDataAccessConfigRejectsUnsupportedCatalogValues(t *testing.T) {
	path := writeDataAccessConfig(t, validDataAccessYAML, 0o600)
	cfg, err := loadDataAccessConfig(path)
	require.NoError(t, err)

	_, err = cfg.resolveKline("stock", "", "1m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data type")
	_, err = cfg.resolveKline("crypto", "okx", "1m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exchange")
	_, err = cfg.resolveKline("crypto", "binance", "5m")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "interval")
}
