package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

type businessFreshnessInventoryProvider struct {
	snapshot monmetrics.TaskResultInventorySnapshot
	err      error
}

func (p *businessFreshnessInventoryProvider) FetchTaskResultInventory(context.Context) (monmetrics.TaskResultInventorySnapshot, error) {
	if p.err != nil {
		return monmetrics.TaskResultInventorySnapshot{}, p.err
	}
	return p.snapshot, nil
}

func TestBusinessFreshnessReporterResolvesDatasetNoLongerExpected(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	check := &domain.Check{
		SpaceID: "crypto", CheckID: "dataset:collector:market_kline:1m",
		Name: "old dataset", GroupName: "business", Kind: domain.CheckKindExternal,
		Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30,
	}
	if err := repositories.Checks.Create(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "no_longer_expected" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterDoesNotResolveMarketCanary(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	check := &domain.Check{
		SpaceID: "crypto", CheckID: "market_canary:market_kline:BTC-USDT:1m:venue:binance",
		Name: "market canary", GroupName: "business", Kind: domain.CheckKindExternal,
		Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30,
	}
	if err := repositories.Checks.Create(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), check.SpaceID, check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("market canary was synthesized by business freshness: %+v", results)
	}
}

func TestBusinessFreshnessReporterCreatesOneKlineGroupCheck(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	labels := `{"space_id":"crypto","view_id":"view","dataset_id":"dataset","subject_id":"BTC","freq":"1m","series_tag":"default"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, row := range []monmetrics.MetricLatest{
			{SeriesID: "data", MetricName: monmetrics.ViewDatasetOutputLastDataTimeMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-10 * time.Minute).Unix()), ObservedAt: now},
		} {
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	evaluator := monmetrics.NewKlineFreshnessEvaluator(query, []monmetrics.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 5 * time.Minute,
	}}, 20)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results, Now: func() time.Time { return now },
	}, repositories, nil, evaluator)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	check, err := repositories.Checks.Get(t.Context(), "crypto", "kline_freshness:crypto:view:1m")
	if err != nil {
		t.Fatal(err)
	}
	if check.Name != "K线新鲜度 crypto view 1m" {
		t.Fatalf("check = %+v", check)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || !strings.HasPrefix(results[0].ErrorMessage, "business_data_stale") || !strings.Contains(results[0].BodyExcerpt, "stale_count=1") {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterReportsKlineFailureWithoutObservation(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	check := &domain.Check{SpaceID: "crypto", CheckID: "kline_freshness:crypto:view:1m", Name: "kline", GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30}
	if err := repositories.Checks.Create(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluator := monmetrics.NewKlineFreshnessEvaluator(query, []monmetrics.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 5 * time.Minute,
	}}, 20)
	run := buildBusinessFreshnessReporterWithInterval(&monitorobservability.Builder{Metrics: query, Checks: repositories.Checks, Results: repositories.Results}, repositories, nil, 5*time.Minute, evaluator)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), check.SpaceID, check.CheckID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || !strings.HasPrefix(results[0].ErrorMessage, "no_observation") {
		t.Fatalf("no-observation kline check result = %+v", results)
	}
}

func TestBusinessFreshnessReporterPersistsInventoryFailureWithoutResolvingKlineCheck(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	provider := &businessFreshnessInventoryProvider{snapshot: monmetrics.TaskResultInventorySnapshot{
		ID: "inventory-1", ObservedAt: now,
		Entries: []monmetrics.TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a", ViewID: "view-a", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now,
		}},
	}}
	cache, err := monmetrics.NewTaskResultInventoryCache(provider, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	evaluator := monmetrics.NewKlineFreshnessEvaluatorWithInventory(query, cache, 2*time.Minute, 20)
	builder := &monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Now: func() time.Time { return now },
	}
	run := buildBusinessFreshnessReporterWithInterval(builder, repositories, nil, time.Second, evaluator)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	klineID := "kline_freshness:crypto:view-a:1m"
	klineResults, err := repositories.Results.Recent(t.Context(), "crypto", klineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(klineResults) != 1 || klineResults[0].Success || !strings.HasPrefix(klineResults[0].ErrorMessage, "no_observation") {
		t.Fatalf("initial Kline result = %+v", klineResults)
	}

	now = now.Add(2 * time.Minute)
	provider.err = errors.New("collector inventory endpoint unavailable")
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	inventoryResults, err := repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, monmetrics.TaskResultInventoryCheckID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventoryResults) != 2 || inventoryResults[0].Success || inventoryResults[0].ErrorMessage != "inventory_refresh_failed" ||
		!strings.Contains(inventoryResults[0].BodyExcerpt, "collector inventory endpoint unavailable") {
		t.Fatalf("inventory refresh result = %+v", inventoryResults)
	}
	klineResults, err = repositories.Results.Recent(t.Context(), "crypto", klineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(klineResults) != 1 || klineResults[0].Success {
		t.Fatalf("inventory failure must preserve the previous Kline failure without synthesizing a resolution: %+v", klineResults)
	}
}

func TestBusinessFreshnessReporterDoesNotResolveKlineCheckDuringTaskPrepare(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	entry := monmetrics.TaskResultInventoryEntry{
		SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a", ViewID: "view-a", Frequency: "1m",
		MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now,
	}
	provider := &businessFreshnessInventoryProvider{snapshot: monmetrics.TaskResultInventorySnapshot{
		ID: "inventory-ready", ObservedAt: now, Entries: []monmetrics.TaskResultInventoryEntry{entry},
	}}
	cache, err := monmetrics.NewTaskResultInventoryCache(provider, time.Minute, 10)
	if err != nil {
		t.Fatal(err)
	}
	evaluator := monmetrics.NewKlineFreshnessEvaluatorWithInventory(query, cache, 2*time.Minute, 20)
	builder := &monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Now: func() time.Time { return now },
	}
	run := buildBusinessFreshnessReporterWithInterval(builder, repositories, nil, time.Second, evaluator)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	klineID := "kline_freshness:crypto:view-a:1m"
	klineResults, err := repositories.Results.Recent(t.Context(), "crypto", klineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(klineResults) != 1 || klineResults[0].Success {
		t.Fatalf("initial Kline failure = %+v", klineResults)
	}

	entry.OwnershipVerified = false
	entry.ResultStatus = "error"
	entry.ObservedAt = now.Add(2 * time.Minute)
	provider.snapshot = monmetrics.TaskResultInventorySnapshot{
		ID: "inventory-error", ObservedAt: entry.ObservedAt, Entries: []monmetrics.TaskResultInventoryEntry{entry},
	}
	now = entry.ObservedAt
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	klineResults, err = repositories.Results.Recent(t.Context(), "crypto", klineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(klineResults) != 2 || klineResults[0].Success || !strings.HasPrefix(klineResults[0].ErrorMessage, "task_result_error") {
		t.Fatalf("unowned task prepare error must remain a Kline failure: %+v", klineResults)
	}

	entry.ResultStatus = "pending"
	entry.ObservedAt = now.Add(2 * time.Minute)
	provider.snapshot = monmetrics.TaskResultInventorySnapshot{
		ID: "inventory-pending", ObservedAt: entry.ObservedAt, Entries: []monmetrics.TaskResultInventoryEntry{entry},
	}
	now = entry.ObservedAt
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	klineResults, err = repositories.Results.Recent(t.Context(), "crypto", klineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(klineResults) != 2 || klineResults[0].Success {
		t.Fatalf("pending preparation must not synthesize a Kline resolution: %+v", klineResults)
	}
}

func TestBusinessFreshnessReporterKeepsCollectorExpectationBeforeStorage(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"bars","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, metric := range []struct {
			id, name, labels string
			value            float64
		}{
			{"enabled", "moox_collector_dataset_enabled", labels, 1},
			{"interval", "moox_collector_dataset_expected_interval_seconds", labels, 60},
			{"inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix())},
		} {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_collector", InstanceID: "collector@node-a", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "moox_collector", InstanceID: "collector@node-a",
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels,
				Value: metric.value, ObservedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", "dataset:collector:bars:1m", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorMessage != "尚未上报" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterUsesStorageViewForCollectorDataset(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		metrics := []struct {
			id, name, labels string
			value            float64
		}{
			{"enabled", "moox_collector_dataset_enabled", `{"dataset_id":"dataset_binance_kline_1m","freq":"1m","space_id":"crypto"}`, 1},
			{"interval", "moox_collector_dataset_expected_interval_seconds", `{"dataset_id":"dataset_binance_kline_1m","freq":"1m","space_id":"crypto"}`, 60},
			{"inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix())},
			{"view-watermark", "moox_storage_view_output_watermark_timestamp_seconds", `{"freq":"1m","space_id":"crypto","view_id":"view_binance_kline_1m"}`, float64(now.Add(-time.Minute).Unix())},
		}
		for _, metric := range metrics {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_collector", InstanceID: "collector@control", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			serviceName := "moox_collector"
			instanceID := "collector@control"
			if strings.HasPrefix(metric.name, "moox_storage_") {
				serviceName = "storage-view"
				instanceID = "storage-view@control"
			}
			if err := db.Model(&monmetrics.MetricSeries{}).Where("c_series_id = ?", metric.id).Updates(map[string]any{
				"c_service_name": serviceName, "c_instance_id": instanceID,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: serviceName, InstanceID: instanceID,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels,
				Value: metric.value, ObservedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", "dataset:collector:dataset_binance_kline_1m:1m", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("collector result should be covered by storage view: %+v", results)
	}
	viewResults, err := repositories.Results.Recent(t.Context(), "crypto", "dataset:storage_view:view_binance_kline_1m:1m", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(viewResults) != 1 || !viewResults[0].Success {
		t.Fatalf("storage view result = %+v", viewResults)
	}
}

func TestBusinessFreshnessReporterStoresBalanceCheckInCryptoMarket(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, metric := range []struct {
			id, name string
			value    float64
		}{
			{"balance-success", "moox_trade_balance_sync_last_success_timestamp_seconds", float64(now.Unix())},
			{"balance-run", "moox_trade_balance_sync_last_run_timestamp_seconds", float64(now.Unix())},
			{"balance-failures", "moox_trade_balance_sync_consecutive_failures", 0},
			{"balance-difference", "moox_trade_balance_sync_max_difference_ratio", 0},
		} {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_trade", InstanceID: "moox_trade@control", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: "{}", LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "moox_trade", InstanceID: "moox_trade@control",
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: "{}", Value: metric.value, ObservedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", "balance:moox_trade", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterCreatesStorageOutboxAlert(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, metric := range []struct {
			id, name string
			value    float64
		}{
			{"outbox-pending", "moox_storage_outbox_pending_entries", 12},
			{"outbox-age", "moox_storage_outbox_oldest_age_seconds", 240},
		} {
			if err := db.Create(&monmetrics.MetricSeries{ServiceName: "storage-node", InstanceID: "storage-node@storage", SeriesID: metric.id, MetricName: metric.name, MetricType: "gauge", LabelsJSON: `{}`, LastSeenAt: now}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{SeriesID: metric.id, ServiceName: "storage-node", InstanceID: "storage-node@storage", MetricName: metric.name, MetricType: "gauge", LabelsJSON: `{}`, Value: metric.value, ObservedAt: now}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Checks: repositories.Checks, Results: repositories.Results, Now: func() time.Time { return now }}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, "data_delivery:storage_outbox", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || !strings.Contains(results[0].ErrorMessage, "积压 12 条") {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterResolvesReporterForDisabledDeployment(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		if err := db.Create(&monmetrics.MetricService{
			ServiceName: "moox_factor", InstanceID: "moox_factor@control", BootID: "old-boot",
			NodeID: "control", LastSeenAt: now.Add(-time.Hour), IsStale: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	for _, check := range []*domain.Check{
		{
			CheckID: "sysdeploy:control:moox_factor", Name: "moox_factor@control",
			Source: domain.CheckSourceSysDeploy, Enabled: false, IntervalSeconds: 30,
		},
		{
			SpaceID: monmetrics.InternalMetricSpaceID,
			CheckID: "reporter:control:moox_factor:moox_factor@control",
			Name:    "Reporter moox_factor control moox_factor@control",
			Source:  domain.CheckSourceObservability, Kind: domain.CheckKindExternal,
			Enabled: true, IntervalSeconds: 30,
		},
	} {
		if err := repositories.Checks.Create(t.Context(), check); err != nil {
			t.Fatal(err)
		}
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(
		t.Context(),
		monmetrics.InternalMetricSpaceID,
		"reporter:control:moox_factor:moox_factor@control",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "no_longer_expected" {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterResolvesDatasetForDisabledProducer(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"factor_output","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		if err := db.Create(&monmetrics.MetricSeries{
			ServiceName: "moox_factor", InstanceID: "moox_factor@control",
			SeriesID: "factor-enabled", MetricName: "moox_factor_dataset_enabled",
			MetricType: "gauge", LabelsJSON: labels, LastSeenAt: now.Add(-time.Hour), IsStale: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&monmetrics.MetricLatest{
			SeriesID: "factor-enabled", ServiceName: "moox_factor", InstanceID: "moox_factor@control",
			MetricName: "moox_factor_dataset_enabled", MetricType: "gauge",
			LabelsJSON: labels, Value: 1, ObservedAt: now.Add(-time.Hour),
		}).Error; err != nil {
			t.Fatal(err)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	for _, check := range []*domain.Check{
		{
			CheckID: "sysdeploy:control:moox_factor", Name: "moox_factor@control",
			Source: domain.CheckSourceSysDeploy, Enabled: false, IntervalSeconds: 30,
		},
		{
			SpaceID: "crypto", CheckID: "dataset:factor:factor_output:1m",
			Name: "Dataset factor factor_output 1m", Source: domain.CheckSourceObservability,
			Kind: domain.CheckKindExternal, Enabled: true, IntervalSeconds: 30,
		},
	} {
		if err := repositories.Checks.Create(t.Context(), check); err != nil {
			t.Fatal(err)
		}
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(
		t.Context(), "crypto", "dataset:factor:factor_output:1m", 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "no_longer_expected" {
		t.Fatalf("results = %+v", results)
	}
}

func TestServiceDeploymentExpectedAcceptsConfiguredLimit(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	rows := make([]domain.Check, 1500)
	for i := range rows {
		rows[i] = domain.Check{
			CheckID: "sysdeploy:node-" + fmt.Sprint(i) + ":service-" + fmt.Sprint(i),
			Source:  domain.CheckSourceSysDeploy, Enabled: false, IntervalSeconds: 30,
		}
	}
	if _, err := store.WithDatabase(manager, func(db *gorm.DB) struct{} {
		if err := db.CreateInBatches(rows, 100).Error; err != nil {
			t.Fatal(err)
		}
		return struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	expected, err := serviceDeploymentExpected(t.Context(), manager.Repositories().Checks, "moox_factor")
	if err != nil || !expected {
		t.Fatalf("expected = %v, err = %v", expected, err)
	}
}

func TestBusinessFreshnessReporterAlertsOncePerStaleReporterAndSuppressesDatasets(t *testing.T) {
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	staleAt := now.Add(-10 * time.Minute)
	labels := `{"dataset_id":"market_kline","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		if err := db.Create(&monmetrics.MetricService{
			ServiceName: "moox_collector", InstanceID: "collector@node-a", BootID: "boot-a",
			NodeID: "node-a", LastSeenAt: staleAt,
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, metric := range []struct {
			id, name, labels string
			value            float64
		}{
			{"enabled", "moox_collector_dataset_enabled", labels, 1},
			{"interval", "moox_collector_dataset_expected_interval_seconds", labels, 60},
			{"inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix())},
			{"run", "moox_collector_dataset_last_run_timestamp_seconds", labels, float64(now.Unix())},
			{"success", "moox_collector_dataset_last_success_timestamp_seconds", labels, float64(now.Unix())},
			{"output", "moox_collector_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix())},
		} {
			if err := db.Create(&monmetrics.MetricSeries{
				ServiceName: "moox_collector", InstanceID: "collector@node-a", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: staleAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "moox_collector", InstanceID: "collector@node-a",
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels,
				Value: metric.value, ObservedAt: staleAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	repositories := manager.Repositories()
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Results: repositories.Results,
		Policy: report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
			RunMissedIntervals: 2, SuccessMissedIntervals: 3,
			WatermarkPeriods: 3, MinimumWatermarkLag: 10 * time.Minute,
		}},
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	enabled := true
	checks, err := repositories.Checks.List(t.Context(), store.ListChecksOptions{
		Source: domain.CheckSourceObservability, Enabled: &enabled,
		Page: store.Page{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	reporterCheckID := "reporter:node-a:moox_collector:collector@node-a"
	if len(checks) != 1 || checks[0].CheckID != reporterCheckID {
		t.Fatalf("checks = %+v", checks)
	}
	results, err := repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, reporterCheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorMessage != "producer stale" {
		t.Fatalf("results = %+v", results)
	}

	if _, err := store.WithDatabase(manager, func(db *gorm.DB) struct{} {
		if err := db.Model(&monmetrics.MetricService{}).
			Where("c_service_name = ? AND c_instance_id = ?", "moox_collector", "collector@node-a").
			Updates(map[string]any{"c_last_seen_at": now, "c_is_stale": false}).Error; err != nil {
			t.Fatal(err)
		}
		return struct{}{}
	}); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err = repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, reporterCheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != "reporter fresh" {
		t.Fatalf("recovery results = %+v", results)
	}
}
