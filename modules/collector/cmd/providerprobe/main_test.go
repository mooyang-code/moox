package main

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestParseRateConcurrencySortsDeduplicatesAndCapsLevels(t *testing.T) {
	levels, err := parseRateConcurrency("4, 1,4,8")
	require.NoError(t, err)
	require.Equal(t, []int{1, 4, 8}, levels)

	_, err = parseRateConcurrency("16")
	require.ErrorContains(t, err, "1..8")
}

func TestP95LatencyMillisecondsUsesUpperRank(t *testing.T) {
	latencies := []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 4 * time.Millisecond}
	require.Equal(t, int64(4), p95LatencyMilliseconds(latencies))
}

func TestProviderProbeMarksUnsupportedExchangeWithoutCallingFeed(t *testing.T) {
	spec := (&testProbeProvider{}).KlineSpec()
	require.True(t, providerSupportsExchange(spec, "XSHG"))
	require.False(t, providerSupportsExchange(spec, "XBSE"))
	entry := unsupportedExchangeEntry("tencent", "920000.XBSE", "bj920000")
	require.NoError(t, entry.Validate())
	require.Equal(t, "unsupported_exchange", entry.ErrorKind)
}

type testProbeProvider struct{}

func (*testProbeProvider) KlineSpec() marketdata.KlineSpec {
	return marketdata.KlineSpec{Exchanges: []string{"XSHG"}}
}
