package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRunOnceRange(t *testing.T) {
	cfg, err := parseArgs([]string{
		"run-once", "--config", "/opt/moox/factor/config/app.yaml",
		"--space", "crypto", "--dataset", "bars", "--subject", "BTC",
		"--freq", "1m", "--start-time", "2026-07-26T00:00:00Z",
		"--end-time", "2026-07-26T01:00:00Z", "--factors", "bias,cci",
	})
	require.NoError(t, err)
	require.Equal(t, "/opt/moox/factor/config/app.yaml", cfg.ConfigPath)
	require.Empty(t, cfg.DBPath)
	require.Empty(t, cfg.FactorsDir)
	require.Equal(t, time.Hour, cfg.EndTime.Sub(cfg.StartTime))
	require.Equal(t, []string{"bias", "cci"}, cfg.FactorIDs)
}

func TestRunOnceCLIPathDefaultsInheritAppConfig(t *testing.T) {
	cfg, err := parseArgs([]string{
		"run-once", "--space", "crypto", "--dataset", "bars", "--subject", "BTC",
		"--freq", "1m", "--start-time", "2026-07-26T00:00:00Z",
		"--end-time", "2026-07-26T01:00:00Z",
	})
	require.NoError(t, err)
	require.Equal(t, "./config/app.yaml", cfg.ConfigPath)
	require.Empty(t, cfg.DBPath)
	require.Empty(t, cfg.FactorsDir)
}

func TestParseImportGenericDefinition(t *testing.T) {
	cfg, err := parseArgs([]string{
		"import", "--file", "./Bias.py", "--factor-id", "bias",
		"--input-columns", "close, benchmark_return", "--outputs", "bias_20,bias_96",
		"--params-json", `{"windows":[20,96]}`, "--lookback-rows", "200",
	})
	require.NoError(t, err)
	require.Equal(t, "./Bias.py", cfg.File)
	require.Equal(t, []string{"close", "benchmark_return"}, cfg.InputColumns)
	require.Equal(t, []string{"bias_20", "bias_96"}, cfg.Outputs)
	require.Equal(t, 200, cfg.LookbackRows)
}

func TestParseImportRejectsStatusFlag(t *testing.T) {
	_, err := parseArgs([]string{
		"import", "--file", "./Bias.py", "--factor-id", "bias",
		"--input-columns", "close", "--outputs", "bias",
		"--lookback-rows", "20", "--status", "enabled",
	})
	require.Error(t, err)
}

func TestParseImportRejectsBlankColumnTokens(t *testing.T) {
	tests := [][]string{
		{"--input-columns", "close,,", "--outputs", "bias"},
		{"--input-columns", "close", "--outputs", ",bias,,"},
	}
	for _, flags := range tests {
		args := append([]string{"import"}, flags...)
		_, err := parseArgs(args)
		require.Error(t, err)
	}
}

func TestReplayCommandIsRemoved(t *testing.T) {
	_, err := parseArgs([]string{"replay"})
	require.Error(t, err)
}
