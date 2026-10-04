package report

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestDatasetModuleObserverRecordsTupleAndModuleRunWithoutFoldingWatermarks(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := NewDatasetMetrics(registry, "collector")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "market_kline", Freq: "1m"}
	if err := datasets.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	module, err := NewModuleMetrics(registry, "collector", []string{"collector-market-data"})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewDatasetModuleObserver(datasets, module, "collect", "collector-market-data")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := observer.ObserveRun(DatasetObservation{
		Key: key, Result: "success", Rows: 1, FinishedAt: now,
		InputWatermark: now.Add(-time.Minute), OutputWatermark: now,
	}); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	requireMetricFamily(t, families, "moox_collector_dataset_output_watermark_timestamp_seconds")
	requireMetricFamily(t, families, ModuleMetricName("collector", ModuleMetricLastSuccess))
	requireNoMetricFamily(t, families, ModuleMetricName("collector", ModuleMetricBusinessWatermark))
}

func TestDatasetModuleObserverDoesNotRecordRejectedDatasetFactAsModuleSuccess(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := NewDatasetMetrics(registry, "collector")
	if err != nil {
		t.Fatal(err)
	}
	module, err := NewModuleMetrics(registry, "collector", []string{"collector-market-data"})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewDatasetModuleObserver(datasets, module, "collect", "collector-market-data")
	if err != nil {
		t.Fatal(err)
	}
	err = observer.ObserveRun(DatasetObservation{
		Key:        DatasetKey{SpaceID: "crypto", DatasetID: "not-enabled", Freq: "1m"},
		Result:     "success",
		FinishedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected disabled Dataset observation to fail")
	}
	families, gatherErr := registry.Gather()
	if gatherErr != nil {
		t.Fatal(gatherErr)
	}
	requireNoMetricFamily(t, families, ModuleMetricName("collector", ModuleMetricLastSuccess))
}

func TestDatasetModuleObserverRecordsIncompleteDatasetAsModuleError(t *testing.T) {
	registry := prometheus.NewRegistry()
	datasets, err := NewDatasetMetrics(registry, "factor")
	if err != nil {
		t.Fatal(err)
	}
	key := DatasetKey{SpaceID: "crypto", DatasetID: "factor_result", Freq: "1m"}
	if err := datasets.ReplaceExpected([]DatasetExpectation{{Key: key, Interval: time.Minute}}); err != nil {
		t.Fatal(err)
	}
	module, err := NewModuleMetrics(registry, "factor", []string{"factor-realtime"})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := NewDatasetModuleObserver(datasets, module, "calculate", "factor-realtime")
	if err != nil {
		t.Fatal(err)
	}

	if err := observer.ObserveRun(DatasetObservation{
		Key: key, Result: "incomplete", FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := testutil.ToFloat64(datasets.runs.WithLabelValues("crypto", "factor_result", "1m", "incomplete")); got != 1 {
		t.Fatalf("incomplete dataset runs=%v, want 1", got)
	}
	if got := testutil.ToFloat64(module.runs.WithLabelValues("calculate", "error", "factor-realtime")); got != 1 {
		t.Fatalf("module error runs=%v, want 1", got)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	requireNoMetricFamily(t, families, ModuleMetricName("factor", ModuleMetricLastSuccess))
}

func requireNoMetricFamily(t *testing.T, families []*dto.MetricFamily, name string) {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			t.Fatalf("unexpected metric family %q", name)
		}
	}
}
