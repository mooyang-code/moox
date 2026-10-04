package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCanonicalBinanceSmokeTargetsSpotAndSwapSeparately(t *testing.T) {
	targets := smokeSeriesTargets("dataset_binance_kline_1m", "1m")
	require.Equal(t, []smokeSeriesTarget{
		{name: "spot", seriesTag: "venue:binance|market:spot|source:spot_http"},
		{name: "swap", seriesTag: "venue:binance|market:swap|source:swap_http"},
	}, targets)
}

func TestCanonicalBinanceSmokeRequiresRowsForBothSeries(t *testing.T) {
	targets := smokeSeriesTargets("dataset_binance_kline_1m", "1m")
	require.NoError(t, validateSmokeSeriesCounts(targets,
		map[string]int{"spot": 1, "swap": 1}, map[string]int{"spot": 1, "swap": 1},
	))
	require.ErrorContains(t, validateSmokeSeriesCounts(targets,
		map[string]int{"swap": 1}, map[string]int{"spot": 1, "swap": 1},
	), "primary query returned no rows for spot")
	require.ErrorContains(t, validateSmokeSeriesCounts(targets,
		map[string]int{"spot": 1, "swap": 1}, map[string]int{"spot": 1},
	), "View query returned no rows for swap")
}
