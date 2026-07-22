package report

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestModuleMetricsRejectsTwoHundredFiftySeventhSeries(t *testing.T) {
	pipelines := make([]string, 32)
	for i := range pipelines {
		pipelines[i] = fmt.Sprintf("pipeline-%d", i)
	}
	m, err := NewModuleMetrics(prometheus.NewRegistry(), "monitor", pipelines)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, stage := range []string{"ingest", "publish", "dispatch", "calculate", "evaluate", "materialize", "reconcile", "rebalance", "collect"} {
		for _, pipeline := range pipelines {
			err := m.SetBacklog(stage, pipeline, 0)
			if created < MaxModuleMetricSeries && err != nil {
				t.Fatalf("series %d rejected early: %v", created+1, err)
			}
			created++
			if created == MaxModuleMetricSeries+1 {
				if err == nil {
					t.Fatal("257th series was accepted")
				}
				return
			}
		}
	}
	t.Fatal("test did not create 257 series")
}

func TestModuleMetricsRejectsUnknownLabelsAndWatermarkRegression(t *testing.T) {
	m, err := NewModuleMetrics(prometheus.NewRegistry(), "collector", []string{"market-data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RecordRun("collect", "success", "market-data", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { return m.RecordRun("storage_commit", "success", "market-data", time.Now()) },
		func() error { return m.RecordRun("collect", "unknown", "market-data", time.Now()) },
		func() error { return m.RecordRun("collect", "success", "user-supplied", time.Now()) },
	} {
		if err := call(); err == nil {
			t.Fatal("invalid metric labels were accepted")
		}
	}
	watermark := time.Unix(100, 0)
	if err := m.AdvanceWatermark("collect", "market-data", watermark); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceWatermark("collect", "market-data", watermark.Add(-time.Second)); err == nil {
		t.Fatal("watermark regression was accepted")
	}
	if err := m.AdvanceInputWatermark("collect", "market-data", watermark); err != nil {
		t.Fatal(err)
	}
	if err := m.AdvanceInputWatermark("collect", "market-data", watermark.Add(-time.Second)); err == nil {
		t.Fatal("input watermark regression was accepted")
	}
}

func TestModuleMetricsEnforcesSeriesLimit(t *testing.T) {
	m, err := NewModuleMetrics(prometheus.NewRegistry(), "monitor", []string{"metrics"})
	if err != nil {
		t.Fatal(err)
	}
	m.maxSeries = 1
	if err := m.SetBacklog("ingest", "metrics", 1); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordRun("ingest", "success", "metrics", time.Now()); err == nil {
		t.Fatal("series limit was not enforced")
	}
}

func TestModuleMetricErrorsAreExposed(t *testing.T) {
	before := testutil.ToFloat64(moduleMetricErrors.WithLabelValues("factor", "run"))
	err := recordModuleMetricError("factor", "run", fmt.Errorf("series limit exceeded"))
	if err == nil {
		t.Fatal("expected metric error")
	}
	if got := testutil.ToFloat64(moduleMetricErrors.WithLabelValues("factor", "run")); got != before+1 {
		t.Fatalf("metric error counter = %v, want %v", got, before+1)
	}
}
