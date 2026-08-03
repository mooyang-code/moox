package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMetricsExposeCompactLowCardinalitySet(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.Observe("crypto", &marketfetchpb.MarketFetchBatchCompleted{
		DatasetId:   "binance_spot_kline_1m",
		Frequency:   "1m",
		Status:      "succeeded",
		DurationMs:  250,
		CompletedAt: timestamppb.Now(),
	})
	metrics.SetRetryPending("crypto", "binance_spot_kline_1m", "1m", 0)
	families, err := registry.Gather()
	require.NoError(t, err)

	got := make(map[string]struct{}, len(families))
	for _, family := range families {
		got[family.GetName()] = struct{}{}
	}
	want := map[string]struct{}{
		"moox_collector_market_fetch_batches_total":                  {},
		"moox_collector_market_fetch_batch_duration_seconds":         {},
		"moox_collector_market_fetch_retry_pending":                  {},
		"moox_collector_market_fetch_last_success_timestamp_seconds": {},
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("metric %q is missing; got %v", name, got)
		}
	}
	if _, ok := got["moox_collector_market_fetch_items_total"]; ok {
		t.Fatal("per-item counter should not be registered")
	}
}

func TestMetricsReportTerminalBatchToDatasetFreshness(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := report.NewDatasetMetrics(registry, "collector")
	require.NoError(t, err)
	key := report.DatasetKey{SpaceID: "crypto_market", DatasetID: "binance_spot_kline_1m", Freq: "1m"}
	require.NoError(t, datasets.ReplaceExpected([]report.DatasetExpectation{{Key: key, Interval: time.Minute}}))
	module, err := report.NewModuleMetrics(registry, "collector", []string{"collector-market-data"})
	require.NoError(t, err)
	observer, err := report.NewDatasetModuleObserver(datasets, module, "collect", "collector-market-data")
	require.NoError(t, err)

	completedAt := time.Date(2026, time.August, 3, 3, 30, 0, 0, time.UTC)
	metrics := NewMetrics(registry)
	metrics.SetDatasetRunObserver(observer)
	metrics.Observe("crypto_market", &marketfetchpb.MarketFetchBatchCompleted{
		DatasetId: "binance_spot_kline_1m", Frequency: "1m", Status: "succeeded", CompletedAt: timestamppb.New(completedAt),
	})

	require.Equal(t, float64(completedAt.Unix()), metricGaugeValue(t, registry, "moox_collector_dataset_last_run_timestamp_seconds", map[string]string{"space_id": "crypto_market", "dataset_id": "binance_spot_kline_1m", "freq": "1m"}))
	require.Equal(t, float64(completedAt.Unix()), metricGaugeValue(t, registry, "moox_collector_dataset_last_success_timestamp_seconds", map[string]string{"space_id": "crypto_market", "dataset_id": "binance_spot_kline_1m", "freq": "1m"}))
}

func metricGaugeValue(t *testing.T, registry *prometheus.Registry, name string, wantLabels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if len(labels) == len(wantLabels) {
				matches := true
				for key, value := range wantLabels {
					if labels[key] != value {
						matches = false
						break
					}
				}
				if matches {
					return metric.GetGauge().GetValue()
				}
			}
		}
	}
	t.Fatalf("metric %s with labels %v is missing", name, wantLabels)
	return 0
}

func TestMetricsUseFixedOutcomeAndLabelContracts(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	for _, status := range []string{"succeeded", "partial_failed", "failed", "timed_out"} {
		metrics.Observe("crypto", &marketfetchpb.MarketFetchBatchCompleted{DatasetId: "bars", Frequency: "1m", Status: status})
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		switch family.GetName() {
		case "moox_collector_market_fetch_batches_total":
			if got := len(family.GetMetric()); got != 4 {
				t.Fatalf("batch outcome series=%d, want 4", got)
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "outcome" {
						switch label.GetValue() {
						case "success", "partial_failed", "failed", "timeout":
						default:
							t.Fatalf("unexpected outcome label %q", label.GetValue())
						}
					}
				}
			}
		}
	}
}
