package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validDataAccessYAML = `version: 1
access:
  address: 146.56.196.204:11004
  id: access@storage
  caller: moox-skill
  key: moox-skill-1:skill-secret
storage:
  app_id: moox-skill
  app_key: storage-key
data_types:
  crypto:
    default_exchange: binance
    exchanges:
      binance:
        space_id: crypto
        series_tag: venue:binance|market:spot|source:spot_http
        kline_datasets:
          1m: dataset_binance_kline_1m
  stockcn:
    default_exchange: stockcn
    exchanges:
      stockcn:
        space_id: stockcn
        series_tag: default
        kline_datasets:
          1m: dataset_stockcn_equity_kline_1m
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
	assert.Equal(t, "146.56.196.204:11004", cfg.Access.Address)
	assert.Equal(t, "access@storage", cfg.Access.ID)
	selection, err := cfg.resolveKline(" CRYPTO ", "", " 1M ")
	require.NoError(t, err)
	assert.Equal(t, "binance", selection.Exchange)
	assert.Equal(t, "crypto", selection.SpaceID)
	assert.Equal(t, "dataset_binance_kline_1m", selection.DatasetID)
	assert.Equal(t, "venue:binance|market:spot|source:spot_http", selection.SeriesTag)

	selection, err = cfg.resolveKline(" stockcn ", "", " 1M ")
	require.NoError(t, err)
	assert.Equal(t, "stockcn", selection.Exchange)
	assert.Equal(t, "stockcn", selection.SpaceID)
	assert.Equal(t, "dataset_stockcn_equity_kline_1m", selection.DatasetID)
	assert.Equal(t, "default", selection.SeriesTag)
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

func TestDataAccessConfigRejectsInvalidAccess(t *testing.T) {
	for _, tc := range []struct{ old, new, field string }{
		{"address: 146.56.196.204:11004", "address: ip://146.56.196.204:11004", "access.address"},
		{"address: 146.56.196.204:11004", "address: 146.56.196.204", "access.address"},
		{"id: access@storage", "id: storage", "access.id"},
		{"key: moox-skill-1:skill-secret", "key: skill-secret", "access.key"},
		{"caller: moox-skill", "caller: \"\"", "access.caller"},
	} {
		t.Run(tc.new, func(t *testing.T) {
			content := strings.Replace(validDataAccessYAML, tc.old, tc.new, 1)
			_, err := loadDataAccessConfig(writeDataAccessConfig(t, content, 0o600))
			require.ErrorContains(t, err, tc.field)
		})
	}
}

func TestDataAccessConfigBuildsAccessModeGateway(t *testing.T) {
	cfg, err := loadDataAccessConfig(writeDataAccessConfig(t, validDataAccessYAML, 0o600))
	require.NoError(t, err)
	gateway, err := cfg.newGateway()
	require.NoError(t, err)
	defer gateway.Close()
	assert.Equal(t, "moox-skill", gateway.Caller())
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
