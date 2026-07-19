package report

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

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
