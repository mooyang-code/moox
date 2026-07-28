package report

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestModuleMetricsNamesAndLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	m, err := NewModuleMetrics(registry, "collector", []string{"market-data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ObserveRun("collect", "success", "market-data", time.Now()); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	requireMetricFamily(t, families, "moox_collector_runs_total")
	requireNoLabels(t, families, "module", "subject_id", "job_id", "error")
	for _, family := range families {
		if !strings.HasPrefix(family.GetName(), "moox_collector_") {
			t.Fatalf("metric family %q does not use module prefix", family.GetName())
		}
	}
}

func TestModuleMetricsRejectsInvalidModule(t *testing.T) {
	for _, module := range []string{"", "Collector", "collector.bad", "collector-bad"} {
		if _, err := NewModuleMetrics(prometheus.NewRegistry(), module, nil); err == nil {
			t.Fatalf("module %q was accepted", module)
		}
	}
}

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
		for _, result := range []string{"success", "error", "rejected"} {
			for _, pipeline := range pipelines {
				err := m.ObserveRun(stage, result, pipeline, time.Time{})
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
	}
	t.Fatal("test did not create 257 series")
}

func TestModuleMetricsRejectsUnknownLabelsAndWatermarkRegression(t *testing.T) {
	m, err := NewModuleMetrics(prometheus.NewRegistry(), "collector", []string{"market-data"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ObserveRun("collect", "success", "market-data", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, call := range []func() error{
		func() error { return m.ObserveRun("storage_commit", "success", "market-data", time.Now()) },
		func() error { return m.ObserveRun("collect", "unknown", "market-data", time.Now()) },
		func() error { return m.ObserveRun("collect", "success", "user-supplied", time.Now()) },
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
	if err := m.ObserveRun("ingest", "success", "metrics", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := m.ObserveRun("ingest", "error", "metrics", time.Now()); err == nil {
		t.Fatal("series limit was not enforced")
	}
}

func TestModuleMetricErrorsUseModulePrefix(t *testing.T) {
	registry := prometheus.NewRegistry()
	m, err := NewModuleMetrics(registry, "factor", []string{"factor-run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ObserveRun("bad-stage", "success", "factor-run", time.Now()); err == nil {
		t.Fatal("expected metric error")
	}
	if got := testutil.ToFloat64(m.errors.WithLabelValues("run")); got != 1 {
		t.Fatalf("metric error counter = %v, want 1", got)
	}
}

func requireMetricFamily(t *testing.T, families []*dto.MetricFamily, name string) {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return
		}
	}
	t.Fatalf("metric family %q not found", name)
}

func requireNoLabels(t *testing.T, families []*dto.MetricFamily, forbidden ...string) {
	t.Helper()
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				for _, name := range forbidden {
					if label.GetName() == name {
						t.Fatalf("metric family %q contains forbidden label %q", family.GetName(), name)
					}
				}
			}
		}
	}
}
