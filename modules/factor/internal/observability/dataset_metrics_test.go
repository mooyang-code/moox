package observability

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestFactorDatasetWatermarksDoNotRegressOnReplay(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry)
	require.NoError(t, err)
	key := report.DatasetKey{SpaceID: "crypto", DatasetID: "bars_factor", Freq: "1m"}
	require.NoError(t, metrics.ReplaceExpected([]report.DatasetExpectation{{
		Key: key, Interval: time.Minute,
	}}))
	t1005 := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	t1003 := t1005.Add(-2 * time.Minute)

	require.NoError(t, metrics.ObserveRun(report.DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: t1005,
		InputWatermark: t1005, OutputWatermark: t1005,
	}))
	require.NoError(t, metrics.ObserveRun(report.DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: t1005.Add(time.Second),
		InputWatermark: t1003, OutputWatermark: t1003,
	}))

	require.Equal(t, float64(t1005.Unix()), factorGaugeValue(
		t, registry, "moox_factor_dataset_input_watermark_timestamp_seconds",
	))
	require.Equal(t, float64(t1005.Unix()), factorGaugeValue(
		t, registry, "moox_factor_dataset_output_watermark_timestamp_seconds",
	))
}

func factorGaugeValue(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == name && len(family.GetMetric()) > 0 {
			return family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
}
