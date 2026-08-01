package bootstrap

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmarketfetch "github.com/mooyang-code/moox/modules/monitor/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

func TestMarketFetchResultDetectsStaleCompletionAndRetryBacklog(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	status := monmarketfetch.Status{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", Status: "succeeded", CompletedAt: now.Add(-3 * time.Minute), RetryCount: 2}
	result := marketFetchResult(marketFetchCheck(status), status, now)
	require.False(t, result.Success)
	require.Equal(t, domain.CheckStatusDown, result.Status)
	require.Contains(t, result.ErrorMessage, "超过允许新鲜度")
	require.Contains(t, result.ErrorMessage, "待重试 2 项")
}

func TestMarketFetchResultAcceptsFreshSuccess(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	status := monmarketfetch.Status{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", Status: "succeeded", CompletedAt: now.Add(-time.Minute)}
	result := marketFetchResult(marketFetchCheck(status), status, now)
	require.True(t, result.Success)
	require.Equal(t, domain.CheckStatusOK, result.Status)
}
