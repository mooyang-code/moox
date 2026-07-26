package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRunOnceRange(t *testing.T) {
	cfg, err := parseArgs([]string{
		"run-once", "--space", "crypto", "--dataset", "bars", "--subject", "BTC",
		"--freq", "1m", "--start-time", "2026-07-26T00:00:00Z",
		"--end-time", "2026-07-26T01:00:00Z", "--factors", "bias,cci",
	})
	require.NoError(t, err)
	require.Equal(t, time.Hour, cfg.EndTime.Sub(cfg.StartTime))
	require.Equal(t, []string{"bias", "cci"}, cfg.FactorIDs)
}

func TestParseImportDefaultPeriods(t *testing.T) {
	cfg, err := parseArgs([]string{"import", "--default-periods", "20,96"})
	require.NoError(t, err)
	require.Equal(t, []int{20, 96}, cfg.DefaultPeriods)
}

func TestReplayCommandIsRemoved(t *testing.T) {
	_, err := parseArgs([]string{"replay"})
	require.Error(t, err)
}
