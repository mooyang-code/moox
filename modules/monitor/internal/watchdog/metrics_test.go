package watchdog

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricsObserveUsesOnlyKindAndResultLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewMetrics(registry)
	require.NoError(t, err)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	metrics.Observe(
		domain.Check{CheckID: "secret-id", Kind: domain.CheckKindHTTP},
		domain.CheckResult{Success: true, LatencyMS: 250, CheckedAt: now},
	)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.checks.WithLabelValues("http", "success")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(metrics.lastRun.WithLabelValues("http")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(metrics.lastSuccess.WithLabelValues("http")))

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				require.NotEqual(t, "check_id", label.GetName())
				require.NotEqual(t, "node_id", label.GetName())
				require.NotEqual(t, "service_name", label.GetName())
			}
		}
	}
}
