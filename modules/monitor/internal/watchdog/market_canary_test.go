package watchdog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateMarketCanaryChecksFreshnessBeforeMovement(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	cfg := MarketCanaryConfig{Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 4}

	insufficient, err := EvaluateMarketCanary(now, []MarketBar{{DataTime: now.Add(-time.Minute), Close: 100, Volume: 2, Closed: true}}, cfg)
	require.NoError(t, err)
	require.False(t, insufficient.Fresh)
	require.Equal(t, "insufficient_closed_bars", insufficient.Reason)

	stale, err := EvaluateMarketCanary(now, []MarketBar{
		{DataTime: now.Add(-6 * time.Minute), Close: 100, Volume: 2, Closed: true},
		{DataTime: now.Add(-5 * time.Minute), Close: 200, Volume: 20, Closed: true},
	}, cfg)
	require.NoError(t, err)
	require.False(t, stale.Fresh)
	require.False(t, stale.Abnormal)
	require.Equal(t, "stale_watermark", stale.Reason)
}

func TestEvaluateMarketCanaryDetectsPriceOrVolumeThreshold(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	cfg := MarketCanaryConfig{Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 4}
	result, err := EvaluateMarketCanary(now, []MarketBar{
		{DataTime: now.Add(-2 * time.Minute), Close: 100, Volume: 2, Closed: true},
		{DataTime: now.Add(-time.Minute), Close: 106, Volume: 3, Closed: true},
	}, cfg)
	require.NoError(t, err)
	require.True(t, result.Fresh)
	require.True(t, result.Abnormal)
	require.InDelta(t, 0.06, result.Return, 0.0001)

	result, err = EvaluateMarketCanary(now, []MarketBar{
		{DataTime: now.Add(-2 * time.Minute), Close: 100, Volume: 1, Closed: true},
		{DataTime: now.Add(-time.Minute), Close: 101, Volume: 5, Closed: true},
	}, cfg)
	require.NoError(t, err)
	require.True(t, result.Abnormal)
	require.InDelta(t, 5, result.VolumeRatio, 0.0001)
}

func TestEvaluateMarketCanaryIgnoresOpenBars(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	result, err := EvaluateMarketCanary(now, []MarketBar{
		{DataTime: now.Add(-2 * time.Minute), Close: 100, Volume: 1, Closed: true},
		{DataTime: now.Add(-time.Minute), Close: 200, Volume: 20, Closed: false},
	}, MarketCanaryConfig{Freshness: 3 * time.Minute, ReturnThreshold: 0.05, VolumeRatioThreshold: 4})
	require.NoError(t, err)
	require.False(t, result.Fresh)
	require.Equal(t, "insufficient_closed_bars", result.Reason)
}
