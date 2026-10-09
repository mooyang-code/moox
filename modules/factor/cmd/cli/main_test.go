package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func TestParseImport(t *testing.T) {
	cfg, err := parseArgs([]string{
		"import", "--set", "fset_bars_1m", "--file", "./Bias.py", "--factor-id", "Bias",
		"--inputs", "close, volume", "--outputs", "bias", "--lookback", "20",
		"--params", `{"window":20}`,
	})
	require.NoError(t, err)
	require.Equal(t, "fset_bars_1m", cfg.SetID)
	require.Equal(t, []string{"close", "volume"}, cfg.InputColumns)
	require.Equal(t, []string{"bias"}, cfg.Outputs)
	require.Equal(t, 20, cfg.LookbackPeriods)
	require.Equal(t, "timeseries", cfg.FactorType)
}

func TestParseImportDefinitionsAndMembers(t *testing.T) {
	// A definition can be imported on its own; --set additionally adds a disabled member.
	alone, err := parseArgs([]string{"import", "--file", "./Bias.py", "--factor-id", "Bias", "--inputs", "close", "--outputs", "bias", "--lookback", "5"})
	require.NoError(t, err)
	require.Empty(t, alone.SetID)

	catalogOnly, err := parseArgs([]string{"import-catalog", "--dir", "/opt/factors"})
	require.NoError(t, err)
	require.Empty(t, catalogOnly.SetID)
}

func TestParseImportRejectsBlankColumns(t *testing.T) {
	_, err := parseArgs([]string{"import", "--set", "s", "--file", "f.py", "--factor-id", "f", "--inputs", "close,,open", "--outputs", "value", "--lookback", "1"})
	require.Error(t, err)
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

func TestImportCatalogCreatesDefinitionsOnly(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to validate factor sources")
	}
	root := t.TempDir()
	factorsDir := filepath.Join(root, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"),
		[]byte("def compute(df, params, context):\n    return df\n"), 0o600))
	// The catalog has no set or status field: it only describes definitions.
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "catalog.json"), []byte(`[
		{"factor_type":"timeseries","file":"Bias.py","factor_id":"Bias","input_columns":["close"],
		 "outputs":["bias_5"],"params":{"window":5},"lookback_periods":5}
	]`), 0o600))
	dbPath := filepath.Join(root, "factor.db")
	configPath := filepath.Join(root, "app.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("database:\n  path: "+dbPath+"\npython:\n  bin: python3\n"), 0o600))

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
	require.Len(t, result.Imported, 1)
	require.Equal(t, "Bias", result.Imported[0].FactorID)
	require.Empty(t, result.Imported[0].MemberStatus)

	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	defer db.Close()
	defs, err := db.ListFactors(t.Context())
	require.NoError(t, err)
	require.Len(t, defs, 1)
	usages, err := db.ListUsages(t.Context(), "Bias")
	require.NoError(t, err)
	require.Empty(t, usages["Bias"])
}
