package observability

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestStorageDatasetWatermarkDoesNotRegressOnReplay(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry)
	require.NoError(t, err)
	key := report.DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	t1005 := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	t1003 := t1005.Add(-2 * time.Minute)

	require.NoError(t, metrics.ObserveRun(report.DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: t1005, OutputWatermark: t1005,
	}))
	require.NoError(t, metrics.ObserveRun(report.DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: t1005.Add(time.Second), OutputWatermark: t1003,
	}))

	require.Equal(t, float64(t1005.Unix()), datasetGaugeValue(
		t, registry, "moox_storage_dataset_output_watermark_timestamp_seconds",
	))
}

func TestStorageDatasetMetricsBoundActuallyAttemptedTupleUnion(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := newDatasetMetrics(registry, 2)
	require.NoError(t, err)
	finishedAt := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	for _, datasetID := range []string{"one", "two"} {
		require.NoError(t, metrics.ObserveRun(report.DatasetObservation{
			Key: report.DatasetKey{
				SpaceID: "crypto", DatasetID: datasetID, Freq: "1m",
			},
			Result: "error", FinishedAt: finishedAt,
		}))
	}
	err = metrics.ObserveRun(report.DatasetObservation{
		Key: report.DatasetKey{
			SpaceID: "crypto", DatasetID: "three", Freq: "1m",
		},
		Result: "error", FinishedAt: finishedAt,
	})
	require.ErrorContains(t, err, "series limit 2")
	require.Len(t, metrics.expected, 2)
}

func TestStorageDatasetMetricsRejectInvalidFrequency(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	err = metrics.ObserveRun(report.DatasetObservation{
		Key: report.DatasetKey{
			SpaceID: "crypto", DatasetID: "market_kline", Freq: "invalid",
		},
		Result: "error", FinishedAt: time.Now(),
	})
	require.ErrorContains(t, err, "freq")
	require.Empty(t, metrics.expected)
}

func datasetGaugeValue(t *testing.T, registry *prometheus.Registry, name string) float64 {
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
