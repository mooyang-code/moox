package observability

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestMetricsRegistered(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	require.NotNil(t, metrics)
	metrics.PeriodDuration.WithLabelValues("set", "plan")
	metrics.PeriodLag.WithLabelValues("set")
	metrics.PeriodTotal.WithLabelValues("set", "complete")
	metrics.Failures.WithLabelValues("set", "factor", "error")
	metrics.LaneBacklog.WithLabelValues("set")
	metrics.LastPeriodTime.WithLabelValues("set")
	families, err := registry.Gather()
	require.NoError(t, err)
	got := make(map[string]struct{}, len(families))
	for _, family := range families {
		got[family.GetName()] = struct{}{}
	}
	for _, name := range []string{
		"factor_period_duration_seconds", "factor_period_lag_seconds", "factor_period_total",
		"factor_failures_total", "factor_lane_backlog", "factor_python_busy", "factor_last_period_time",
	} {
		require.Contains(t, got, name)
	}
}
