package report

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDatasetMetricsNamesAndLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	requireMetricFamily(t, families, "moox_collector_dataset_enabled")
	requireMetricFamily(t, families, "moox_collector_dataset_expected_interval_seconds")
	requireNoLabels(t, families, "module", "subject_id", "job_id", "error")
}

func TestDatasetMetricsReplaceExpectedIsAtomic(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "factor")
	if err != nil {
		t.Fatal(err)
	}
	valid := DatasetExpectation{
		Key:      DatasetKey{SpaceID: "crypto", DatasetID: "factor_momentum", Freq: "1m"},
		Interval: time.Minute,
	}
	if err := metrics.ReplaceExpected([]DatasetExpectation{valid}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{
		Key:      DatasetKey{SpaceID: "crypto", DatasetID: "bad dataset", Freq: "1m"},
		Interval: time.Minute,
	}}); err == nil {
		t.Fatal("invalid replacement was accepted")
	}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues("crypto", "factor_momentum", "1m")); got != 1 {
		t.Fatalf("previous expected dataset was cleared: %v", got)
	}
}

func TestDatasetMetricsReplaceExpectedPublishesDisabledTombstone(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ReplaceExpected(nil); err != nil {
		t.Fatal(err)
	}
	labels := []string{"crypto", "market_kline", "1m"}
	if got := testutil.ToFloat64(metrics.enabled.WithLabelValues(labels...)); got != 0 {
		t.Fatalf("disabled tombstone = %v, want 0", got)
	}
	if got := testutil.ToFloat64(metrics.expectedInterval.WithLabelValues(labels...)); got != 0 {
		t.Fatalf("disabled interval = %v, want 0", got)
	}
}

func TestDatasetMetricsRecordsUpstreamInventoryRefreshError(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "factor")
	if err != nil {
		t.Fatal(err)
	}
	metrics.ObserveInventoryRefreshError()
	if got := testutil.ToFloat64(metrics.inventoryRefreshErrors); got != 1 {
		t.Fatalf("inventory refresh errors = %v, want 1", got)
	}
}

func TestDatasetMetricsObserveRunUsesMaxWatermarks(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	newer := time.Unix(200, 0)
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 2, FinishedAt: time.Unix(210, 0),
		InputWatermark: newer, OutputWatermark: newer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Unix(220, 0),
		InputWatermark: newer.Add(-time.Minute), OutputWatermark: newer.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	labels := []string{"crypto", "market_kline", "1m"}
	if got := testutil.ToFloat64(metrics.inputWatermark.WithLabelValues(labels...)); got != 200 {
		t.Fatalf("input watermark regressed to %v", got)
	}
	if got := testutil.ToFloat64(metrics.outputWatermark.WithLabelValues(labels...)); got != 200 {
		t.Fatalf("output watermark regressed to %v", got)
	}
	if got := testutil.ToFloat64(metrics.rows.WithLabelValues("crypto", "market_kline", "1m", "success")); got != 3 {
		t.Fatalf("rows total = %v, want 3", got)
	}
}

func TestDatasetMetricsAcceptsCanonicalStorageFrequency(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "binance_spot_kline_1h", Freq: "1H"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Hour}}); err != nil {
		t.Fatalf("canonical Storage frequency rejected: %v", err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("canonical Storage frequency observation rejected: %v", err)
	}
}

func TestDatasetMetricsUsesCanonicalFrequencyIdentity(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	lowercase := DatasetKey{SpaceID: "crypto", DatasetID: "binance_spot_kline_1h", Freq: "1h"}
	canonical := DatasetKey{SpaceID: "crypto", DatasetID: "binance_spot_kline_1h", Freq: "1H"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: lowercase, Interval: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key: canonical, Result: "success", Rows: 1, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("canonical observation did not match lowercase inventory: %v", err)
	}
}

func TestDatasetMetricsRejectsUnknownDatasetAndLabels(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.ObserveRun(DatasetObservation{
		Key:    DatasetKey{SpaceID: "crypto", DatasetID: "other", Freq: "1m"},
		Result: "success", FinishedAt: time.Now(),
	}); err == nil {
		t.Fatal("unknown dataset was accepted")
	}
	if err := metrics.ObserveRun(DatasetObservation{Key: key, Result: "credential leaked", FinishedAt: time.Now()}); err == nil {
		t.Fatal("unbounded result label was accepted")
	}
}

func TestDatasetMetricsObserveFactDoesNotDeclareEnabledInventory(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewDatasetMetrics(registry, "storage")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ObserveFact(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: time.Now().UTC(),
		OutputWatermark: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "moox_storage_dataset_enabled" {
			t.Fatal("fact-only Storage observation declared an enabled Dataset")
		}
	}
	requireMetricFamily(t, families, "moox_storage_dataset_output_watermark_timestamp_seconds")
}

func TestDatasetMetricsAcceptsEmptyWithoutExpandingModuleResults(t *testing.T) {
	metrics, err := NewDatasetMetrics(prometheus.NewRegistry(), "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := metrics.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	finishedAt := time.Now().UTC().Truncate(time.Second)
	if err := metrics.ObserveRun(DatasetObservation{Key: key, Result: "empty", FinishedAt: finishedAt}); err != nil {
		t.Fatalf("empty dataset result rejected: %v", err)
	}
	if got := testutil.ToFloat64(metrics.lastSuccess.WithLabelValues("crypto", "market_kline", "1m")); got != float64(finishedAt.Unix()) {
		t.Fatalf("empty last success = %v, want %d", got, finishedAt.Unix())
	}
	module, err := NewModuleMetrics(prometheus.NewRegistry(), "collector", []string{"market-data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.ObserveRun("collect", "empty", "market-data", time.Now()); err == nil {
		t.Fatal("module metrics accepted dataset-only empty result")
	}
}
