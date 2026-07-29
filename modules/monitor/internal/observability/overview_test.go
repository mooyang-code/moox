package observability

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
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
	for _, freq := range []string{"1h", "1H"} {
		key := datasetKey{spaceID: "crypto", datasetID: "market_kline", freq: freq}
		runLag, successLag, watermarkLag := datasetTolerances(key, 60, testRealtimePolicy())
		if runLag != 2*time.Minute+30*time.Second {
			t.Fatalf("%s run lag = %s", freq, runLag)
		}
		if successLag != 3*time.Minute+30*time.Second {
			t.Fatalf("%s success lag = %s", freq, successLag)
		}
		if watermarkLag != 3*time.Hour {
			t.Fatalf("%s watermark lag = %s", freq, watermarkLag)
		}
	}
}

func TestDatasetTolerancesMatchFrequencyAliasesInOverrides(t *testing.T) {
	policy := testRealtimePolicy()
	policy.Overrides = []report.RealtimeTimeSeriesOverride{{
		SpaceID: "crypto", DatasetID: "market_kline", Freq: "1h", WatermarkLag: 90 * time.Minute,
	}}

	_, _, watermarkLag := datasetTolerances(
		datasetKey{spaceID: "crypto", datasetID: "market_kline", freq: "1H"},
		60,
		policy,
	)

	if watermarkLag != 90*time.Minute {
		t.Fatalf("watermark lag = %s", watermarkLag)
	}
}

func TestDatasetStatusMarksCanonicalFrequencyWatermarkStale(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	got := datasetStatus(now, datasetKey{
		producer: "collector", spaceID: "crypto", datasetID: "market_kline", freq: "1H",
	}, datasetValues{
		interval: 3600, inventory: float64(now.Unix()), lastRun: float64(now.Unix()),
		lastSuccess: float64(now.Unix()), output: float64(now.Add(-4 * time.Hour).Unix()),
	}, testRealtimePolicy())

	if got.Status != "stale" || got.Reason != "watermark stale" {
		t.Fatalf("status = %+v", got)
	}
}

func TestDatasetStatusRejectsStaleInventory(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	got := datasetStatus(now, datasetKey{producer: "collector", spaceID: "crypto", datasetID: "market_kline", freq: "1m"}, datasetValues{
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

func TestBuilderDeduplicatesServiceBootHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, repositories := openOverviewState(t, func(db *gorm.DB) {
		for _, service := range []monmetrics.MetricService{
			{
				ServiceName: "moox_collector", InstanceID: "collector@node-a", BootID: "boot-old",
				NodeID: "node-a", LastSeenAt: now.Add(-10 * time.Minute),
			},
			{
				ServiceName: "moox_collector", InstanceID: "collector@node-a", BootID: "boot-new",
				NodeID: "node-a", LastSeenAt: now,
			},
		} {
			if err := db.Create(&service).Error; err != nil {
				t.Fatal(err)
			}
		}
	})
	got, err := (Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "healthy" ||
		!got.Services[0].LastSeenAt.Equal(now) {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderSysDeployFailureOverridesFreshReporter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query, repositories := openOverviewState(t, func(db *gorm.DB) {
		if err := db.Create(&monmetrics.MetricService{
			ServiceName: "moox_storage", InstanceID: "storage@node-a", BootID: "boot-a",
			NodeID: "node-a", LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	})
	check := domain.Check{
		SpaceID: "moox_system", CheckID: "sysdeploy:node-a:moox_storage",
		Name: "moox_storage@node-a", Kind: domain.CheckKindHTTP,
		Source: domain.CheckSourceSysDeploy, Enabled: true,
		Labels: `{"node_id":"node-a","service_name":"moox_storage"}`,
	}
	if err := repositories.Checks.Create(t.Context(), &check); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: "storage-down", SpaceID: check.SpaceID, CheckID: check.CheckID,
		Status: domain.CheckStatusDown, ErrorMessage: "readyz failed", CheckedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := (Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "down" ||
		!strings.Contains(got.Services[0].Reason, "readyz failed") ||
		got.Services[0].ReporterStatus != "healthy" {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderIncludesSysDeployServiceWithoutReporter(t *testing.T) {
	query, repositories := openOverviewState(t, func(*gorm.DB) {})
	check := domain.Check{
		SpaceID: "moox_system", CheckID: "sysdeploy:node-b:moox_factor",
		Name: "moox_factor@node-b", Kind: domain.CheckKindHTTP,
		Source: domain.CheckSourceSysDeploy, Enabled: true,
		Labels: `{"node_id":"node-b","service_name":"moox_factor"}`,
	}
	if err := repositories.Checks.Create(t.Context(), &check); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: "factor-ready", SpaceID: check.SpaceID, CheckID: check.CheckID,
		Success: true, Status: domain.CheckStatusOK, CheckedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := (Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "unknown" ||
		got.Services[0].ReporterStatus != "missing" ||
		!strings.Contains(got.Services[0].Reason, "reporter missing") {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestBuilderDoesNotRequireReporterFromHealthOnlyService(t *testing.T) {
	query, repositories := openOverviewState(t, func(*gorm.DB) {})
	check := domain.Check{
		SpaceID: "moox_system", CheckID: "sysdeploy:node-b:web_host",
		Name: "web_host@node-b", Kind: domain.CheckKindHTTP,
		Source: domain.CheckSourceSysDeploy, Enabled: true,
		Labels: `{"node_id":"node-b","service_name":"web_host"}`,
	}
	if err := repositories.Checks.Create(t.Context(), &check); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Results.Insert(t.Context(), &domain.CheckResult{
		ResultID: "web-ready", SpaceID: check.SpaceID, CheckID: check.CheckID,
		Success: true, Status: domain.CheckStatusOK, CheckedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := (Builder{Metrics: query, Checks: repositories.Checks, Results: repositories.Results}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Services) != 1 || got.Services[0].Status != "healthy" || got.Services[0].ReporterStatus != "" {
		t.Fatalf("services = %+v", got.Services)
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

func TestBuilderMarksBalanceDownAfterThreeFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", `{}`, float64(now.Add(-8*time.Minute).Unix()), now)
		seedOverviewMetric(t, db, "balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "balance-failures", "moox_trade_balance_sync_consecutive_failures", `{}`, 3, now)
		seedOverviewMetric(t, db, "balance-difference", "moox_trade_balance_sync_max_difference_ratio", `{}`, 0.01, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, BalanceDifferenceThreshold: 0.05}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BusinessChecks) != 1 || got.BusinessChecks[0].Status != "down" ||
		!strings.Contains(got.BusinessChecks[0].Reason, "3 consecutive") {
		t.Fatalf("balance status = %+v", got.BusinessChecks)
	}
}

func TestBuilderMarksBalanceDifferenceAboveThresholdDown(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "balance-failures", "moox_trade_balance_sync_consecutive_failures", `{}`, 0, now)
		seedOverviewMetric(t, db, "balance-difference", "moox_trade_balance_sync_max_difference_ratio", `{}`, 0.08, now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, BalanceDifferenceThreshold: 0.05}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BusinessChecks) != 1 || got.BusinessChecks[0].Status != "down" ||
		!strings.Contains(got.BusinessChecks[0].Reason, "exceeds") {
		t.Fatalf("balance status = %+v", got.BusinessChecks)
	}
}

func TestBuilderIncludesStorageCommitFactsWithoutEnabledInventory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"market_kline","freq":"1m","space_id":"crypto"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetric(t, db, "collector-enabled", "moox_collector_dataset_enabled", labels, 1, now)
		seedOverviewMetric(t, db, "collector-interval", "moox_collector_dataset_expected_interval_seconds", labels, 300, now)
		seedOverviewMetric(t, db, "collector-inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "collector-run", "moox_collector_dataset_last_run_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "collector-success", "moox_collector_dataset_last_success_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "collector-output", "moox_collector_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-enabled", "moox_factor_dataset_enabled", labels, 1, now)
		seedOverviewMetric(t, db, "factor-interval", "moox_factor_dataset_expected_interval_seconds", labels, 600, now)
		seedOverviewMetric(t, db, "factor-inventory", "moox_factor_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-run", "moox_factor_dataset_last_run_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-success", "moox_factor_dataset_last_success_timestamp_seconds", labels, float64(now.Unix()), now)
		seedOverviewMetric(t, db, "factor-output", "moox_factor_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix()), now)
		storageAt := now.Add(-3 * time.Minute)
		seedOverviewMetric(t, db, "storage-run", "moox_storage_dataset_last_run_timestamp_seconds", labels, float64(storageAt.Unix()), now)
		seedOverviewMetric(t, db, "storage-success", "moox_storage_dataset_last_success_timestamp_seconds", labels, float64(storageAt.Unix()), now)
		seedOverviewMetric(t, db, "storage-output", "moox_storage_dataset_output_watermark_timestamp_seconds", labels, float64(storageAt.Unix()), now)
	})
	got, err := (Builder{Metrics: query, Now: func() time.Time { return now }, Policy: testRealtimePolicy()}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	var storageRow *DatasetFrequencyStatus
	for i := range got.Datasets {
		if got.Datasets[i].Producer == "storage" {
			storageRow = &got.Datasets[i]
		}
	}
	if len(got.Datasets) != 3 || storageRow == nil ||
		storageRow.DatasetID != "market_kline" || storageRow.Status != "healthy" {
		t.Fatalf("Storage fact row = %+v", got.Datasets)
	}
}

func TestBuilderAggregatesDatasetInstances(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"market_kline","freq":"1m","space_id":"crypto"}`
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		for _, instance := range []struct {
			id        string
			seenAt    time.Time
			lastRun   time.Time
			inventory time.Time
		}{
			{id: "collector-a", seenAt: now.Add(-10 * time.Minute), lastRun: now.Add(-10 * time.Minute), inventory: now.Add(-10 * time.Minute)},
			{id: "collector-b", seenAt: now, lastRun: now, inventory: now},
		} {
			seedOverviewMetricForInstance(t, db, instance.id+"-enabled", "moox_collector", instance.id, "moox_collector_dataset_enabled", labels, 1, instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-interval", "moox_collector", instance.id, "moox_collector_dataset_expected_interval_seconds", labels, 60, instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-inventory", "moox_collector", instance.id, "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(instance.inventory.Unix()), instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-run", "moox_collector", instance.id, "moox_collector_dataset_last_run_timestamp_seconds", labels, float64(instance.lastRun.Unix()), instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-success", "moox_collector", instance.id, "moox_collector_dataset_last_success_timestamp_seconds", labels, float64(instance.lastRun.Unix()), instance.seenAt)
			seedOverviewMetricForInstance(t, db, instance.id+"-output", "moox_collector", instance.id, "moox_collector_dataset_output_watermark_timestamp_seconds", labels, float64(instance.lastRun.Unix()), instance.seenAt)
		}
	})
	got, err := (Builder{
		Metrics: query, Now: func() time.Time { return now }, Policy: testRealtimePolicy(),
	}).Build(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Datasets) != 1 || got.Datasets[0].Status != "healthy" ||
		!got.Datasets[0].LastRunAt.Equal(now) {
		t.Fatalf("datasets = %+v", got.Datasets)
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
	query, _ := openOverviewState(t, seed)
	return query
}

func openOverviewState(t *testing.T, seed func(*gorm.DB)) (*monmetrics.QueryService, *store.Repositories) {
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
	return query, manager.Repositories()
}

func seedOverviewMetric(t *testing.T, db *gorm.DB, id, name, labels string, value float64, observedAt time.Time) {
	t.Helper()
	seedOverviewMetricForInstance(t, db, id, "moox_cloudnode", "cloudnode@node-a", name, labels, value, observedAt)
}

func seedOverviewMetricForInstance(
	t *testing.T,
	db *gorm.DB,
	id, serviceName, instanceID, name, labels string,
	value float64,
	observedAt time.Time,
) {
	t.Helper()
	if err := db.Create(&monmetrics.MetricSeries{
		ServiceName: serviceName, InstanceID: instanceID, SeriesID: id,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, LastSeenAt: observedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&monmetrics.MetricLatest{
		SeriesID: id, ServiceName: serviceName, InstanceID: instanceID,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, Value: value, ObservedAt: observedAt,
	}).Error; err != nil {
		t.Fatal(err)
	}
}
