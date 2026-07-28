package observability

import (
	"strings"
	"testing"
	"time"
)

func TestDatasetStatusDistinguishesMissingStaleAndEmpty(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	key := datasetKey{producer: "collector", spaceID: "moox_system", datasetID: "market_1m", freq: "1m"}

	missing := datasetStatus(now, key, datasetValues{interval: 60})
	if missing.Status != "unknown" || missing.Reason != "尚未上报" {
		t.Fatalf("missing = %+v", missing)
	}
	stale := datasetStatus(now, key, datasetValues{
		interval: 60, lastRun: float64(now.Add(-10 * time.Minute).Unix()),
		lastSuccess: float64(now.Add(-10 * time.Minute).Unix()), output: float64(now.Add(-10 * time.Minute).Unix()),
	})
	if stale.Status != "stale" || stale.Reason != "watermark stale" || stale.LagSeconds != 600 {
		t.Fatalf("stale = %+v", stale)
	}
	empty := datasetStatus(now, key, datasetValues{
		interval: 60, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	})
	if empty.Status != "healthy" || empty.Reason != "正常但空结果" {
		t.Fatalf("empty = %+v", empty)
	}
	producerStale := datasetStatus(now, key, datasetValues{
		reporterStale: true, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	})
	if producerStale.Status != "stale" || producerStale.Reason != "producer stale" {
		t.Fatalf("producer stale = %+v", producerStale)
	}
}

func TestOverviewSortsAbnormalRowsFirstAndSummarizesSCF(t *testing.T) {
	now := time.Now().UTC()
	overview := Overview{
		Services: []ServiceStatus{
			{ServiceName: "healthy-service", Status: "healthy", LastSeenAt: now},
			{ServiceName: "moox_collector_scf", Status: "stale", LastSeenAt: now.Add(-time.Minute)},
			{ServiceName: "scf-secondary", Status: "healthy", LastSeenAt: now},
		},
		Datasets: []DatasetFrequencyStatus{
			{DatasetID: "ok", Status: "healthy"},
			{DatasetID: "late", Status: "stale"},
		},
	}
	overview.SCF = summarizeSCF(overview.Services)
	sortOverview(&overview)
	if overview.Services[0].Status != "stale" || overview.Datasets[0].Status != "stale" {
		t.Fatalf("abnormal rows were not first: %+v", overview)
	}
	if overview.SCF.OnlineCount != 1 || overview.SCF.TimeoutCount != 1 || overview.SCF.UnknownCount != 0 {
		t.Fatalf("SCF summary = %+v", overview.SCF)
	}
	if !overview.SCF.OldestHeartbeatAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("oldest heartbeat = %s", overview.SCF.OldestHeartbeatAt)
	}
}

func TestDatasetFrequencyLimitReturnsErrorInsteadOfTruncating(t *testing.T) {
	err := ensureDatasetLimit(MaxDatasetFrequencyStatuses + 1)
	if err == nil || !strings.Contains(err.Error(), "exceed limit 1000") {
		t.Fatalf("limit error = %v", err)
	}
}

func TestBuilderReturnsBoundedEmptyOverviewWhenSourcesAreDisabled(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	got, err := (Builder{Now: func() time.Time { return now }}).Build(t.Context(), "moox_system")
	if err != nil {
		t.Fatal(err)
	}
	if !got.GeneratedAt.Equal(now) || len(got.Services) != 0 || len(got.Hosts) != 0 || len(got.Datasets) != 0 {
		t.Fatalf("overview = %+v", got)
	}
}
