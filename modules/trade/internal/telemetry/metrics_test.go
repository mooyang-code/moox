package telemetry

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestExecutionMetricsCountsChildrenPerCurrentTarget(t *testing.T) {
	metrics := &ExecutionMetrics{}
	metrics.ChildPlaced("logical-x", "target-1", false)
	metrics.ChildPlaced("logical-x", "target-1", true)
	require.Equal(t, 2.0, testutil.ToFloat64(TargetChildOrders.WithLabelValues("logical-x")))
	require.Equal(t, 1.0, testutil.ToFloat64(Reversals.WithLabelValues("logical-x")))
	metrics.ChildPlaced("logical-x", "target-2", false)
	require.Equal(t, 1.0, testutil.ToFloat64(TargetChildOrders.WithLabelValues("logical-x")), "新目标到达时子单数从零算")
	metrics.Blocked("account-x", OrderDuplicateBlocked)
	require.Equal(t, 1.0, testutil.ToFloat64(Orders.WithLabelValues("account-x", OrderDuplicateBlocked)))
}

func TestUnknownResultsSinceDropsOldEntries(t *testing.T) {
	now := time.Unix(1_000, 0)
	for index := 0; index < 6; index++ {
		NoteUnknownResult("account-u", now.Add(time.Duration(index)*time.Second))
	}
	NoteUnknownResult("account-u", now.Add(-2*time.Minute))
	counts := UnknownResultsSince(now.Add(-time.Minute))
	require.Equal(t, 6, counts["account-u"])
	require.Empty(t, UnknownResultsSince(now.Add(time.Hour)))
}

func TestNoteKeepsDetailWithinWindow(t *testing.T) {
	now := time.Unix(2_000, 0)
	Note(AttentionRateLimited, "fapi.binance.com：请求过于频繁", now)
	require.Equal(t, []string{"fapi.binance.com：请求过于频繁"}, Recent(AttentionRateLimited, now.Add(time.Minute), 5*time.Minute))
	require.Empty(t, Recent(AttentionRateLimited, now.Add(10*time.Minute), 5*time.Minute))
}
