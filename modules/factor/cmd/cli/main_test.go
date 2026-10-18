package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestParseImport(t *testing.T) {
	cfg, err := parseArgs([]string{"import", "--set", "fset_bars_1m", "--file", "./xbx.yaml", "--factor-id", "Bias"})
	require.NoError(t, err)
	require.Equal(t, "fset_bars_1m", cfg.SetID)
	require.Equal(t, "./xbx.yaml", cfg.File)
	require.Equal(t, "Bias", cfg.FactorID)

	alone, err := parseArgs([]string{"import", "--file", "./xbx.yaml"})
	require.NoError(t, err)
	require.Empty(t, alone.SetID)

	for _, retired := range []string{"--inputs", "--outputs", "--lookback", "--params", "--factor-type"} {
		_, err := parseArgs([]string{"import", "--file", "./xbx.yaml", retired, "x"})
		require.Error(t, err, retired)
	}
}

func TestParseImportCatalogUsesDirectoryAndSet(t *testing.T) {
	cfg, err := parseArgs([]string{"import-catalog", "--set", "fset_bars_1m", "--dir", "/opt/factors"})
	require.NoError(t, err)
	require.Equal(t, "fset_bars_1m", cfg.SetID)
	require.Equal(t, "/opt/factors", cfg.CatalogDir)
}

func TestParseImportCatalogDefaultsToFactorsDirectory(t *testing.T) {
	cfg, err := parseArgs([]string{"import-catalog"})
	require.NoError(t, err)
	require.Equal(t, "./factors", cfg.CatalogDir)
}

func TestParseRecalcRangeAndSelectors(t *testing.T) {
	cfg, err := parseArgs([]string{
		"recalc", "--set", "fset_bars_1m", "--start", "2026-10-04T00:00:00Z",
		"--end", "2026-10-04T01:00:00Z", "--factor", "Bias", "--factor", "Cci",
		"--subject", "BTC,ETH",
	})
	require.NoError(t, err)
	require.Equal(t, time.Hour, cfg.EndTime.Sub(cfg.StartTime))
	require.Equal(t, []string{"Bias", "Cci"}, cfg.FactorIDs)
	require.Equal(t, []string{"BTC", "ETH"}, cfg.Subjects)
	require.Empty(t, cfg.DBPath)
}

func TestStatusRequiresFactorTarget(t *testing.T) {
	_, err := parseArgs([]string{"status"})
	require.NoError(t, err)
	var output bytes.Buffer
	err = runStatus(t.Context(), cliConfig{}, &output)
	require.ErrorContains(t, err, "FactorMgr target is required")
}

func TestLegacyCLICommandsAreRemoved(t *testing.T) {
	for _, command := range []string{"recalc-cancel", "recalc-status", "clear-queue", "replay", "run-once"} {
		_, err := parseArgs([]string{command})
		require.ErrorContains(t, err, "unknown command")
	}
}

func TestRunInitCreatesFactorSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "factor.db")
	var output bytes.Buffer
	require.NoError(t, runInit(cliConfig{DBPath: dbPath}, &output))
	var result struct {
		OK     bool     `json:"ok"`
		Tables []string `json:"tables"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.True(t, result.OK)
	require.Equal(t, []string{"t_factor_sets", "t_factor_defs", "t_factor_set_members", "t_factor_recalc_jobs"}, result.Tables)
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

const testLibrary = `- factor_id: Bias
  name: Bias
  spec: |
    params: {window: 5}
    outputs:
      bias_{window}: close / ts_mean(close, window)
- factor_id: Cci
  name: CCI
  spec: |
    outputs:
      cci: close
`

func TestImportCatalogCreatesDefinitionsOnly(t *testing.T) {
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	// The library has no set or status field: it only describes definitions.
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "lib.yaml"), []byte(testLibrary), 0o600))
	dbPath := filepath.Join(root, "factor.db")
	configPath := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("database:\n  path: "+dbPath+"\n"), 0o600))

	var output bytes.Buffer
	require.NoError(t, runImportCatalog(t.Context(), cliConfig{ConfigPath: configPath, CatalogDir: factorsDir}, &output))
	var result struct {
		OK       bool `json:"ok"`
		Imported []struct {
			FactorID     string `json:"factor_id"`
			MemberStatus string `json:"member_status"`
		} `json:"imported"`
	}
	require.NoError(t, json.Unmarshal(output.Bytes(), &result))
	require.True(t, result.OK)
	require.Len(t, result.Imported, 2)
	require.Equal(t, "Bias", result.Imported[0].FactorID)
	require.Empty(t, result.Imported[0].MemberStatus)

	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	defs, err := db.ListFactors(t.Context())
	require.NoError(t, err)
	require.Len(t, defs, 2)
	require.Equal(t, []string{"bias_5"}, defs[0].Outputs)
	usages, err := db.ListUsages(t.Context(), "Bias")
	require.NoError(t, err)
	require.Empty(t, usages["Bias"])
	require.NoError(t, db.Close())

	output.Reset()
	err = runImportCatalog(t.Context(), cliConfig{ConfigPath: configPath, CatalogDir: factorsDir}, &output)
	require.ErrorContains(t, err, "already exists", "重复导入不覆盖已有定义")
}

func TestImportSelectsOneDefinitionFromFile(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "lib.yaml")
	require.NoError(t, os.WriteFile(library, []byte(testLibrary), 0o600))
	dbPath := filepath.Join(root, "factor.db")
	configPath := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("database:\n  path: "+dbPath+"\n"), 0o600))

	var output bytes.Buffer
	err := runImport(t.Context(), cliConfig{ConfigPath: configPath, File: library}, &output)
	require.ErrorContains(t, err, "--factor-id", "文件里有多个定义时必须指定")
	err = runImport(t.Context(), cliConfig{ConfigPath: configPath, File: library, FactorID: "Missing"}, &output)
	require.ErrorContains(t, err, "没有因子 Missing")
	require.NoError(t, runImport(t.Context(), cliConfig{ConfigPath: configPath, File: library, FactorID: "Cci"}, &output))
	require.Contains(t, output.String(), `"definition_hash":"sha256:`)
}
