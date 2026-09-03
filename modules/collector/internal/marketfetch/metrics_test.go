package marketfetch

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func TestMetricsExposeCompactAssignmentSet(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveAssignment("crypto_market", "bars", "1m", 16, 16, 1722652200)
	metrics.ObserveAssignmentPending("crypto_market", true, time.Unix(1722652200, 0))
	metrics.ObserveTimerState("crypto_market", "timer-1", "true", 1)
	metrics.ObserveTimerCapacity("crypto_market", 45, 52, 0)
	metrics.ObserveAssignmentError("crypto_market", "capacity")
	metrics.ObserveAssignmentFailure("crypto_market", "submit_timeout")
	metrics.ObservePeriodPending("bars", "1m", 2)
	metrics.ObservePeriodReportRetry("bars", "1m")
	families, err := registry.Gather()
	require.NoError(t, err)

	got := make(map[string]struct{}, len(families))
	for _, family := range families {
		got[family.GetName()] = struct{}{}
	}
	want := map[string]struct{}{
		"moox_collector_market_fetch_assignment_required":                          {},
		"moox_collector_market_fetch_assignment_active":                            {},
		"moox_collector_market_fetch_assignment_last_success_timestamp_seconds":    {},
		"moox_collector_market_fetch_coordination_healthy":                         {},
		"moox_collector_market_fetch_coordination_failure":                         {},
		"moox_collector_market_fetch_coordination_pending":                         {},
		"moox_collector_market_fetch_coordination_pending_since_timestamp_seconds": {},
		"moox_collector_market_fetch_timer_available":                              {},
		"moox_collector_market_fetch_timer_capacity_total":                         {},
		"moox_collector_market_fetch_timer_capacity_required":                      {},
		"moox_collector_market_fetch_timer_capacity_active":                        {},
		"moox_collector_market_fetch_timer_capacity_headroom":                      {},
		"moox_collector_market_fetch_assignment_errors_total":                      {},
		"moox_collector_period_pending_total":                                      {},
		"moox_collector_period_report_retry_total":                                 {},
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("metric %q is missing; got %v", name, got)
		}
	}
	for name := range got {
		if name == "moox_collector_market_fetch_batches_total" || name == "moox_collector_market_fetch_retry_pending" {
			t.Fatalf("legacy completion metric %q should not be registered", name)
		}
	}
}

func TestMetricsClearAssignmentPending(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentPending("crypto_market", true, time.Unix(1722652200, 0))
	metrics.ObserveAssignmentPending("crypto_market", false, time.Time{})
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto_market")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto_market")))
}

func TestMetricsExposeTimerCapacityHeadroom(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveTimerCapacity("crypto_market", 45, 52, 0)
	require.Equal(t, float64(45), testutil.ToFloat64(metrics.timerCapacityTotal.WithLabelValues("crypto_market")))
	require.Equal(t, float64(52), testutil.ToFloat64(metrics.timerCapacityRequired.WithLabelValues("crypto_market")))
	require.Equal(t, float64(0), testutil.ToFloat64(metrics.timerCapacityActive.WithLabelValues("crypto_market")))
	require.Equal(t, float64(-7), testutil.ToFloat64(metrics.timerCapacityHeadroom.WithLabelValues("crypto_market")))
}

func TestMetricsUseFixedErrorReasons(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	for _, reason := range []string{"capacity", "rules", "symbols", "dns", "cloudnode", "environment"} {
		metrics.ObserveAssignmentError("crypto_market", reason)
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "moox_collector_market_fetch_assignment_errors_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() != "reason" {
					continue
				}
				switch label.GetValue() {
				case "capacity", "rules", "symbols", "dns", "cloudnode", "environment":
				default:
					t.Fatalf("unexpected error reason %q", label.GetValue())
				}
			}
		}
	}
}

func TestMetricsRemoveDeletedAssignmentAndTimerLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveAssignmentDesired("crypto_market", "old-bars", "1m", 1, 1)
	metrics.ObserveTimerState("crypto_market", "old-timer", "true", 1)
	metrics.ResetAssignmentScope("crypto_market")
	metrics.ResetTimerScope("crypto_market")
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "moox_collector_market_fetch_assignment_required" || family.GetName() == "moox_collector_market_fetch_assignment_active" || family.GetName() == "moox_collector_market_fetch_timer_available" {
			if len(family.GetMetric()) > 0 {
				t.Fatalf("expected deleted gauge labels to be absent, family=%s", family.GetName())
			}
		}
	}
}

func TestAssignmentMetricsCountEnvironmentSplits(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"ETH-USDT"}},
	}
	assignments := []NodeAssignment{
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT"}},
		{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"ETH-USDT"}},
	}
	scopes := assignmentMetricScopes(groups, assignments)
	require.Len(t, scopes, 1)
	require.Equal(t, 2, scopes[0].Required)
	require.Equal(t, 2, scopes[0].Active)
}

func TestMetricsRecoveryCanSucceedWithoutAssignmentScopes(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto_market", "rules")
	metrics.ObserveAssignmentSuccess("crypto_market", 1722772800)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto_market")))
}

func TestMetricsKeepOnlyCurrentCoordinationFailureReason(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto_market", "submit_timeout")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto_market", "submit_timeout")))

	metrics.ObserveAssignmentFailure("crypto_market", "cloudnode")
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto_market", "submit_timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto_market", "cloudnode")))

	metrics.ObserveAssignmentSuccess("crypto_market", 1722772800)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto_market", "cloudnode")))
}

func TestMetricsResetRequirementsPreservesLastActiveAssignment(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentDesired("crypto_market", "bars", "1m", 34, 34)
	metrics.ResetAssignmentRequirements("crypto_market")
	metrics.ObserveAssignmentRequired("crypto_market", "bars", "1m", 34)

	require.Equal(t, float64(34), testutil.ToFloat64(metrics.assignmentRequired.WithLabelValues("crypto_market", "bars", "1m")))
	require.Equal(t, float64(34), testutil.ToFloat64(metrics.assignmentActive.WithLabelValues("crypto_market", "bars", "1m")))
}

func TestMetricsExposeLowCardinalityFeedDimensions(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	metrics.ObserveFeedResult(FeedMetric{
		MarketID: "stock_cn", RouteID: StockCNRouteID, ProviderID: "sina",
		FeedKind: "kline", GroupID: 7, GroupCount: 200, BatchKind: "realtime", Result: "success",
	})

	families, err := registry.Gather()
	require.NoError(t, err)
	family := metricFamily(t, families, "moox_collector_market_feed_results_total")
	require.Len(t, family.GetMetric(), 1)
	labels := metricLabels(family.GetMetric()[0])
	require.Equal(t, map[string]string{
		"market_id": "stock_cn", "route_id": StockCNRouteID, "provider_id": "sina",
		"feed_kind": "kline", "group_id": "7", "batch_kind": "realtime", "result": "success",
	}, labels)
	for _, forbidden := range []string{"subject", "subject_id", "ip", "candidate_chain", "function_name"} {
		_, exists := labels[forbidden]
		require.False(t, exists, "high-cardinality label %q must not be exposed", forbidden)
	}
}

func TestMetricsAllowConfiguredCryptoKlineFrequencies(t *testing.T) {
	for _, route := range []string{"binance_spot_kline_1h", "binance_swap_kline_1w"} {
		marketID, bounded := boundedMarketRoute("crypto", route)
		require.Equal(t, "crypto", marketID)
		require.Equal(t, route, bounded)
	}
	_, bounded := boundedMarketRoute("crypto", "binance_spot_kline_2m")
	require.Equal(t, "unknown", bounded)
}

func TestMetricsRejectFeedGroupOutsideConfiguredRange(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveFeedResult(FeedMetric{MarketID: "stock_cn", RouteID: StockCNRouteID, ProviderID: "sina", FeedKind: "kline", GroupID: 200, GroupCount: 200, BatchKind: "realtime", Result: "success"})
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotEqual(t, "moox_collector_market_feed_results_total", family.GetName())
	}
}

func TestMetricsExposeConfiguredGroupsEgressAndInstrumentSnapshot(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveConfiguredGroups("stock_cn", StockCNRouteID, 200, 199)
	metrics.ObserveEgressDiagnostic("stock_cn", StockCNRouteID, 200, 200, 199, 198)
	metrics.ObserveInstrumentSnapshot("stock_cn", StockCNRouteID, "sina", "success", 5180, map[string]int{"XSHG": 2200, "XSHE": 2800, "XBSE": 180}, time.Unix(1722772800, 0))

	require.Equal(t, 200.0, testutil.ToFloat64(metrics.configuredGroups.WithLabelValues("stock_cn", StockCNRouteID, "expected")))
	require.Equal(t, 199.0, testutil.ToFloat64(metrics.configuredGroups.WithLabelValues("stock_cn", StockCNRouteID, "actual")))
	require.Equal(t, 198.0, testutil.ToFloat64(metrics.egressFunctions.WithLabelValues("stock_cn", StockCNRouteID, "distinct_ip")))
	require.Equal(t, 5180.0, testutil.ToFloat64(metrics.instrumentActive.WithLabelValues("stock_cn", StockCNRouteID, "sina", "success")))
	require.Equal(t, 180.0, testutil.ToFloat64(metrics.instrumentExchange.WithLabelValues("stock_cn", StockCNRouteID, "sina", "XBSE")))
}

func metricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}

func metricLabels(metric *dto.Metric) map[string]string {
	labels := make(map[string]string, len(metric.GetLabel()))
	for _, label := range metric.GetLabel() {
		labels[label.GetName()] = label.GetValue()
	}
	return labels
}
