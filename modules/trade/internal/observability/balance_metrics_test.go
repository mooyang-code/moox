package observability

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestBalanceMetricsTracksSuccessAndFailureWithoutAccountLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewBalanceMetrics(registry)
	require.NoError(t, err)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	metrics.Observe(now, 0.03, nil)
	metrics.Observe(now.Add(time.Minute), 0.9, errors.New("venue unavailable"))

	require.Equal(t, float64(1), testutil.ToFloat64(metrics.runs.WithLabelValues("success")))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.runs.WithLabelValues("error")))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(metrics.lastSuccess))
	require.InDelta(t, 0.03, testutil.ToFloat64(metrics.maxDifference), 0.0001)

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				require.NotEqual(t, "account_id", label.GetName())
			}
		}
	}
}
