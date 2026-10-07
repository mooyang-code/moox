package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSmokeTargetsUseConfiguredSeriesTag(t *testing.T) {
	tag := "venue:binance|market:spot|source:spot_http"
	require.Equal(t, []smokeSeriesTarget{{name: tag, seriesTag: tag}}, smokeSeriesTargets(" "+tag+" "))
	require.Equal(t, []smokeSeriesTarget{{name: "unscoped"}}, smokeSeriesTargets(""))
}

func TestSmokeRequiresRowsInPrimaryAndView(t *testing.T) {
	targets := smokeSeriesTargets("")
	require.NoError(t, validateSmokeSeriesCounts(targets, map[string]int{"unscoped": 1}, map[string]int{"unscoped": 1}))
	require.ErrorContains(t, validateSmokeSeriesCounts(targets, map[string]int{}, map[string]int{"unscoped": 1}), "primary query returned no rows for unscoped")
	require.ErrorContains(t, validateSmokeSeriesCounts(targets, map[string]int{"unscoped": 1}, map[string]int{}), "View query returned no rows for unscoped")
}
