package observability

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

func TestDatasetStatusDistinguishesMissingStaleAndEmpty(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	key := datasetKey{producer: "collector", spaceID: "moox_system", datasetID: "market_1m", freq: "1m"}
	policy := testRealtimePolicy()
	inventory := float64(now.Unix())

	missing := datasetStatus(now, key, datasetValues{interval: 60, inventory: inventory}, policy)
	if missing.Status != "unknown" || missing.Reason != "尚未上报" {
		t.Fatalf("missing = %+v", missing)
	}
	stale := datasetStatus(now, key, datasetValues{
		interval: 60, lastRun: float64(now.Add(-10 * time.Minute).Unix()),
		lastSuccess: float64(now.Add(-10 * time.Minute).Unix()), output: float64(now.Add(-10 * time.Minute).Unix()),
		inventory: inventory,
	}, policy)
	if stale.Status != "stale" || stale.Reason != "run stale" || stale.LagSeconds != 600 {
		t.Fatalf("stale = %+v", stale)
	}
	empty := datasetStatus(now, key, datasetValues{
		interval: 60, inventory: inventory, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	}, policy)
	if empty.Status != "healthy" || empty.Reason != "正常但空结果" {
		t.Fatalf("empty = %+v", empty)
	}
	producerStale := datasetStatus(now, key, datasetValues{
		reporterStale: true, inventory: inventory, lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	}, policy)
	if producerStale.Status != "stale" || producerStale.Reason != "producer stale" {
		t.Fatalf("producer stale = %+v", producerStale)
	}
}

func TestDatasetTolerancesUseScheduleForRunsAndFrequencyForWatermark(t *testing.T) {
	key := datasetKey{spaceID: "crypto", datasetID: "market_kline", freq: "1h"}
	runLag, successLag, watermarkLag := datasetTolerances(key, 60, testRealtimePolicy())
	if runLag != 2*time.Minute+30*time.Second {
		t.Fatalf("run lag = %s", runLag)
	}
	if successLag != 3*time.Minute+30*time.Second {
		t.Fatalf("success lag = %s", successLag)
	}
	if watermarkLag != 3*time.Hour {
		t.Fatalf("watermark lag = %s", watermarkLag)
	}
}

func TestDatasetStatusRejectsStaleInventory(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	got := datasetStatus(now, datasetKey{spaceID: "crypto", datasetID: "market_kline", freq: "1m"}, datasetValues{
		interval: 60, inventory: float64(now.Add(-11 * time.Minute).Unix()),
		lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
	}, testRealtimePolicy())
	if got.Status != "unknown" || got.Reason != "inventory_stale" {
		t.Fatalf("status = %+v", got)
	}
}

func testRealtimePolicy() report.RealtimeTimeSeriesPolicy {
	return report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
		RunMissedIntervals: 2, SuccessMissedIntervals: 3, WatermarkPeriods: 3, MinimumWatermarkLag: 10 * time.Minute,
	}}
}

func TestOverviewSortsAbnormalRowsFirst(t *testing.T) {
	now := time.Now().UTC()
	overview := Overview{
		Services: []ServiceStatus{
			{ServiceName: "healthy-service", Status: "healthy", LastSeenAt: now},
			{ServiceName: "failed-service", Status: "stale", LastSeenAt: now.Add(-time.Minute)},
		},
		Datasets: []DatasetFrequencyStatus{
			{DatasetID: "ok", Status: "healthy"},
			{DatasetID: "late", Status: "stale"},
		},
	}
	sortOverview(&overview)
	if overview.Services[0].Status != "stale" || overview.Datasets[0].Status != "stale" {
		t.Fatalf("abnormal rows were not first: %+v", overview)
	}
}

func TestBuilderSummarizesAllSCFHeartbeatsFromCloudNodeMetrics(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "online", "moox_cloudnode_scf_nodes", `{"status":"online"}`, 2, now)
		seedOverviewMetric(t, db, "timeout", "moox_cloudnode_scf_nodes", `{"status":"timeout"}`, 1, now)
		seedOverviewMetric(t, db, "unknown", "moox_cloudnode_scf_nodes", `{"status":"unknown"}`, 1, now)
		seedOverviewMetric(t, db, "oldest", "moox_cloudnode_scf_oldest_heartbeat_age_seconds", `{}`, 120, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.SCF.OnlineCount != 2 || got.SCF.TimeoutCount != 1 || got.SCF.UnknownCount != 1 {
		t.Fatalf("SCF summary = %+v", got.SCF)
	}
	if !got.SCF.OldestHeartbeatAt.Equal(now.Add(-2 * time.Minute)) {
		t.Fatalf("oldest heartbeat = %s", got.SCF.OldestHeartbeatAt)
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

func openOverviewMetrics(t *testing.T, seed func(*gorm.DB)) *monmetrics.QueryService {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		seed(db)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	return query
}

func seedOverviewMetric(t *testing.T, db *gorm.DB, id, name, labels string, value float64, observedAt time.Time) {
	t.Helper()
	if err := db.Create(&monmetrics.MetricSeries{
		ServiceName: "moox_cloudnode", InstanceID: "cloudnode@node-a", SeriesID: id,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, LastSeenAt: observedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&monmetrics.MetricLatest{
		SeriesID: id, ServiceName: "moox_cloudnode", InstanceID: "cloudnode@node-a",
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, Value: value, ObservedAt: observedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}
