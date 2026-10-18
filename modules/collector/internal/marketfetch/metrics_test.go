package marketfetch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type operationalStatsFake struct {
	stats store.OperationalStats
	err   error
}

func (f *operationalStatsFake) OperationalStats(context.Context, string, int) (store.OperationalStats, error) {
	return f.stats, f.err
}

func TestOperationalMetricsZeroResetBoundedLabelsAndFailedRefresh(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := NewMetrics(registry)
	now := time.Now().UTC().Truncate(time.Second)
	f := &operationalStatsFake{stats: store.OperationalStats{DBBytes: 100, WALBytes: 20, Rows: map[string]store.OperationalRowCount{"instances": {Count: 9, Capped: true}, "task-secret": {Count: 1}}}}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "instances")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.storeRowsCapped.WithLabelValues("crypto", "instances")))
	f.err = errors.New("task-secret query failure")
	require.Error(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(time.Hour)))
	require.Equal(t, 9.0, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "instances")))
	require.Equal(t, 0.0, testutil.ToFloat64(m.storeStatsHealthy.WithLabelValues("crypto")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(m.storeStatsLastSuccess.WithLabelValues("crypto")))
	f.err, f.stats = nil, store.OperationalStats{}
	require.NoError(t, m.RefreshOperationalStats(context.Background(), f, "crypto", now.Add(time.Hour)))
	require.Zero(t, testutil.ToFloat64(m.storeRows.WithLabelValues("crypto", "instances")))
	m.ObserveMaintenanceDeletes("crypto", map[string]int64{"period_readiness": 4, "task-secret": 3})
	require.Equal(t, 4.0, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "period_readiness")))
	m.ObserveMaintenanceDeletes("crypto", nil)
	require.Zero(t, testutil.ToFloat64(m.maintenanceDeleted.WithLabelValues("crypto", "period_readiness")))
	m.ObserveMaintenancePass(time.Second, errors.New("task-secret"))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for name, value := range metricLabels(metric) {
				require.NotContains(t, name, "subject")
				require.NotContains(t, name, "task")
				require.NotEqual(t, "task-secret", value)
			}
		}
	}
}

func TestMetricsClearAssignmentPending(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentPending("crypto", true, time.Unix(1722652200, 0))
	metrics.ObserveAssignmentPending("crypto", false, time.Time{})
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPending.WithLabelValues("crypto")))
	require.Zero(t, testutil.ToFloat64(metrics.assignmentPendingSince.WithLabelValues("crypto")))
}

func TestMetricsUseFixedErrorReasons(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	for _, reason := range []string{"capacity", "rules", "symbols", "dns", "cloudnode", "environment"} {
		metrics.ObserveAssignmentError("crypto", reason)
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

func TestMetricsRecoveryCanSucceedWithoutAssignmentScopes(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto", "rules")
	metrics.ObserveAssignmentSuccess("crypto", 1722772800)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentHealthy.WithLabelValues("crypto")))
}

func TestMetricsKeepOnlyCurrentCoordinationFailureReason(t *testing.T) {
	metrics := NewMetrics(prometheus.NewRegistry())
	metrics.ObserveAssignmentFailure("crypto", "submit_timeout")
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "submit_timeout")))

	metrics.ObserveAssignmentFailure("crypto", "cloudnode")
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "submit_timeout")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "cloudnode")))

	metrics.ObserveAssignmentSuccess("crypto", 1722772800)
	require.Zero(t, testutil.ToFloat64(metrics.assignmentFailure.WithLabelValues("crypto", "cloudnode")))
}

func TestMetricsExposeLowCardinalityFeedDimensions(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)

	metrics.ObserveFeedResult(FeedMetric{
		MarketID: "stockcn", RouteID: StockCNRouteID, ProviderID: "sina",
		FeedKind: "kline", GroupID: 7, GroupCount: 200, BatchKind: "realtime", Result: "success",
	})

	families, err := registry.Gather()
	require.NoError(t, err)
	family := metricFamily(t, families, "moox_collector_market_feed_results_total")
	require.Len(t, family.GetMetric(), 1)
	labels := metricLabels(family.GetMetric()[0])
	require.Equal(t, map[string]string{
		"market_id": "stockcn", "route_id": StockCNRouteID, "provider_id": "sina",
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
	metrics.ObserveFeedResult(FeedMetric{MarketID: "stockcn", RouteID: StockCNRouteID, ProviderID: "sina", FeedKind: "kline", GroupID: 200, GroupCount: 200, BatchKind: "realtime", Result: "success"})
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotEqual(t, "moox_collector_market_feed_results_total", family.GetName())
	}
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
