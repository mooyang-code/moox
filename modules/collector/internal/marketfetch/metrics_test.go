package marketfetch

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricsExposeCompactAssignmentSet(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveAssignment("crypto_market", "bars", "1m", 16, 16, 1722652200)
	metrics.ObserveTimerState("crypto_market", "timer-1", "true", 1)
	metrics.ObserveAssignmentError("crypto_market", "capacity")
	metrics.ObservePeriodPending("bars", "1m", 2)
	metrics.ObservePeriodReportRetry("bars", "1m")
	families, err := registry.Gather()
	require.NoError(t, err)

	got := make(map[string]struct{}, len(families))
	for _, family := range families {
		got[family.GetName()] = struct{}{}
	}
	want := map[string]struct{}{
		"moox_collector_market_fetch_assignment_required":                       {},
		"moox_collector_market_fetch_assignment_active":                         {},
		"moox_collector_market_fetch_assignment_last_success_timestamp_seconds": {},
		"moox_collector_market_fetch_coordination_healthy":                      {},
		"moox_collector_market_fetch_timer_available":                           {},
		"moox_collector_market_fetch_assignment_errors_total":                   {},
		"moox_collector_period_pending_total":                                   {},
		"moox_collector_period_report_retry_total":                              {},
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
