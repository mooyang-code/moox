package observability

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestPeriodMetricsExposeViewDrivenSignals(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewPeriodMetrics(reg)
	require.NoError(t, err)
	m.Begin("prices-view", "1m")
	m.ObserveSourceReady("prices-view", "1m", time.Now().Add(-2*time.Second))
	m.ObserveDegraded("prices-view", "1m")
	m.ObserveManifestClear("binding-1")
	m.End("prices-view", "1m")
	m.BeginBatch("prices-view", "1m")
	m.ObserveBatch("prices-view", "1m", "complete", 1, 2, 250*time.Millisecond)
	require.Equal(t, float64(0), testutil.ToFloat64(m.periodRunning.WithLabelValues("prices-view", "1m")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.periodDegraded.WithLabelValues("prices-view", "1m")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.manifestClear.WithLabelValues("binding-1")))
	require.Equal(t, float64(0), testutil.ToFloat64(m.batchRunning.WithLabelValues("prices-view", "1m")))
	require.Equal(t, float64(1), testutil.ToFloat64(m.batchTotal.WithLabelValues("prices-view", "1m", "complete")))
	require.Equal(t, float64(2), testutil.ToFloat64(m.batchFactors.WithLabelValues("prices-view", "1m")))
	families, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	for _, name := range []string{
		"moox_factor_period_running", "moox_factor_period_degraded_total",
		"moox_factor_manifest_clear_total", "moox_factor_source_ready_lag_seconds",
		"moox_factor_batch_running", "moox_factor_batch_total",
		"moox_factor_batch_factor_total", "moox_factor_batch_elapsed_seconds",
	} {
		require.True(t, names[name], "missing %s", name)
	}
}
