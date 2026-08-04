package marketfetch

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestMetricsExposeCompactAssignmentSet(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.ObserveAssignment("crypto_market", "bars", "1m", 16, 16, 1722652200)
	metrics.ObserveAssignmentError("crypto_market", "capacity")
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
		"moox_collector_market_fetch_assignment_errors_total":                   {},
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
