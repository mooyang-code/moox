package main

import (
	"bytes"
	"encoding/json"
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

func TestParseImportRejectsBlankColumns(t *testing.T) {
	_, err := parseArgs([]string{"import", "--set", "s", "--file", "f.py", "--factor-id", "f", "--inputs", "close,,open", "--outputs", "value", "--lookback", "1"})
	require.Error(t, err)
}

func TestParseImportCatalogUsesDirectoryAndSet(t *testing.T) {
	cfg, err := parseArgs([]string{"import-catalog", "--set", "fset_bars_1m", "--dir", "/opt/factors"})
	require.NoError(t, err)
	require.Equal(t, "fset_bars_1m", cfg.SetID)
	require.Equal(t, "/opt/factors", cfg.FactorsDir)
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

func TestParseRunOncePeriod(t *testing.T) {
	cfg, err := parseArgs([]string{"run-once", "--set", "fset_bars_1m", "--period", "2026-10-04T00:10:00Z"})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC), cfg.Period)
	require.Empty(t, cfg.DBPath)
	require.Empty(t, cfg.FactorsDir)
}

func TestStatusRequiresFactorTarget(t *testing.T) {
	_, err := parseArgs([]string{"status"})
	require.NoError(t, err)
	var output bytes.Buffer
	err = runStatus(t.Context(), cliConfig{}, &output)
	require.ErrorContains(t, err, "FactorMgr target is required")
}

func TestLegacyCLICommandsAreRemoved(t *testing.T) {
	for _, command := range []string{"recalc-cancel", "recalc-status", "clear-queue", "replay"} {
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
	require.Equal(t, []string{"t_factor_sets", "t_factor_defs", "t_factor_recalc_jobs"}, result.Tables)
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.Close())
}
