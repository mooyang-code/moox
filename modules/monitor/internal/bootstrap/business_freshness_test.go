package bootstrap

import (
	"context"
	"errors"
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
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
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
		Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != noLongerExpected {
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
		Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
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
	labels := `{"space_id":"crypto","view_id":"view","dataset_id":"dataset","freq":"1m"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, row := range []monmetrics.MetricLatest{
			{SeriesID: "latest", MetricName: monmetrics.ViewOutputLatestMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-10 * time.Minute).Unix()), ObservedAt: now},
			{SeriesID: "subjects", MetricName: monmetrics.ViewOutputSubjectsMetric, MetricType: "gauge", LabelsJSON: labels, Value: 1, ObservedAt: now},
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
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results, Now: func() time.Time { return now },
	}, repositories, nil, evaluator)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	check, err := repositories.Checks.Get(t.Context(), "crypto", "kline_freshness:crypto:view:1m")
	if err != nil {
		t.Fatal(err)
	}
	if check.Name != "view（1分钟） · K线结果" {
		t.Fatalf("check = %+v", check)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || !strings.HasPrefix(results[0].ErrorMessage, "整体停止更新") || !strings.Contains(results[0].BodyExcerpt, "stale_count=1") {
		t.Fatalf("results = %+v", results)
	}
}

func TestBusinessFreshnessReporterReenablesRetiredKlineCheckWhenStillExpected(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	labels := `{"space_id":"crypto","view_id":"view","dataset_id":"dataset","freq":"1m"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, row := range []monmetrics.MetricLatest{
			{SeriesID: "latest", MetricName: monmetrics.ViewOutputLatestMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-10 * time.Minute).Unix()), ObservedAt: now},
			{SeriesID: "subjects", MetricName: monmetrics.ViewOutputSubjectsMetric, MetricType: "gauge", LabelsJSON: labels, Value: 1, ObservedAt: now},
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
	// 关闭 K 线新鲜度监控时被退役（停用）的检查
	retired := &domain.Check{SpaceID: "crypto", CheckID: "kline_freshness:crypto:view:1m", Name: "kline", GroupName: "business", Kind: domain.CheckKindExternal, Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30}
	if err := repositories.Checks.Create(t.Context(), retired); err != nil {
		t.Fatal(err)
	}
	retired.Enabled = false
	if err := repositories.Checks.Update(t.Context(), retired); err != nil {
		t.Fatal(err)
	}
	evaluator := monmetrics.NewKlineFreshnessEvaluator(query, []monmetrics.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view", Frequency: "1m", MarketID: "crypto", StaleAfter: 5 * time.Minute,
	}}, 20)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results, Now: func() time.Time { return now },
	}, repositories, nil, evaluator)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := repositories.Checks.Get(t.Context(), "crypto", "kline_freshness:crypto:view:1m")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("当前任务清单仍期望的 K 线检查必须在重新开启监控后恢复启用")
	}
	if _, err := repositories.Alerts.GetRule(t.Context(), "crypto", "default:kline_freshness:crypto:view:1m"); err != nil {
		t.Fatalf("恢复后默认规则应重建: %v", err)
	}
}

func TestBusinessFreshnessReporterDoesNotResolveSubjectTagCheck(t *testing.T) {
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
		SpaceID: "crypto", CheckID: tagCheckPrefix + "crypto:binance_spot",
		Name: "tag", GroupName: "business", Kind: domain.CheckKindExternal,
		Source: domain.CheckSourceObservability, Enabled: true, IntervalSeconds: 30,
	}
	if err := repositories.Checks.Create(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), check.SpaceID, check.CheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("标签检查被业务新鲜度汇总补写了成功结果: %+v", results)
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
	run := buildBusinessFreshnessReporterWithInterval(&monitorobservability.Builder{Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results}, repositories, nil, 5*time.Minute, evaluator)
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
	if len(results) != 1 || results[0].Success || !strings.HasPrefix(results[0].ErrorMessage, "还没有收到任何结果数据") {
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
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
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
	if len(klineResults) != 1 || klineResults[0].Success || !strings.HasPrefix(klineResults[0].ErrorMessage, "还没有收到任何结果数据") {
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
	// 采集服务不可用时沿用最后一份有效清单继续检查，同时单独告警“控制面数据过期”。
	if len(inventoryResults) != 2 || inventoryResults[0].Success || !strings.HasPrefix(inventoryResults[0].ErrorMessage, "控制面数据过期") ||
		!strings.Contains(inventoryResults[0].BodyExcerpt, "collector inventory endpoint unavailable") {
		t.Fatalf("inventory refresh result = %+v", inventoryResults)
	}
	klineResults, err = repositories.Results.Recent(t.Context(), "crypto", klineID, 10)
	if err != nil {
		t.Fatal(err)
	}
	// 沿用最后一份有效清单继续检查：已有的 K 线失败保持，不会被合成为恢复。
	if len(klineResults) == 0 || klineResults[0].Success {
		t.Fatalf("a stale inventory must keep evaluating the Kline check without synthesizing a resolution: %+v", klineResults)
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
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
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
	if len(klineResults) != 2 || klineResults[0].Success || !strings.HasPrefix(klineResults[0].ErrorMessage, "采集任务的结果状态异常") {
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
				ServiceName: "collector", InstanceID: "collector@node-a", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "collector", InstanceID: "collector@node-a",
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
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", "dataset:collector:bars:1m", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorMessage != "还没有收到任何运行记录" {
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
				ServiceName: "collector", InstanceID: "collector@control", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			serviceName := "collector"
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
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
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
				ServiceName: "trade", InstanceID: "trade@control", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: "{}", LastSeenAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "trade", InstanceID: "trade@control",
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
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
		Now: func() time.Time { return now },
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(t.Context(), "crypto", "balance:trade", 1)
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
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results, Now: func() time.Time { return now }}, repositories, nil)
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

func TestBusinessFreshnessReporterDisablesReporterForDisabledDeployment(t *testing.T) {
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
			ServiceName: "factor-mgr", InstanceID: "factor-mgr@control", BootID: "old-boot",
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
			CheckID: "placement:control:factor-mgr", Name: "factor-mgr@control",
			Source: domain.CheckSourcePlacement, Enabled: false, IntervalSeconds: 30,
		},
		{
			SpaceID: monmetrics.InternalMetricSpaceID,
			CheckID: "reporter:control:factor-mgr",
			Name:    "Reporter factor-mgr control factor-mgr@control",
			Source:  domain.CheckSourceObservability, Kind: domain.CheckKindExternal,
			Enabled: true, IntervalSeconds: 30,
		},
	} {
		if err := repositories.Checks.Create(t.Context(), check); err != nil {
			t.Fatal(err)
		}
	}
	seedBusinessTopology(t, repositories, "control", "factor-mgr", false)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
	}, repositories, nil)
	if err := run(t.Context()); err != nil {
		t.Fatal(err)
	}
	results, err := repositories.Results.Recent(
		t.Context(),
		monmetrics.InternalMetricSpaceID,
		"reporter:control:factor-mgr",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	require.Empty(t, results, "disabling a deployment must not fabricate recovery")
	check, err := repositories.Checks.Get(t.Context(), monmetrics.InternalMetricSpaceID, "reporter:control:factor-mgr")
	require.NoError(t, err)
	require.False(t, check.Enabled)
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
			ServiceName: "factor-mgr", InstanceID: "factor-mgr@control",
			SeriesID: "factor-enabled", MetricName: "moox_factor_dataset_enabled",
			MetricType: "gauge", LabelsJSON: labels, LastSeenAt: now.Add(-time.Hour), IsStale: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&monmetrics.MetricLatest{
			SeriesID: "factor-enabled", ServiceName: "factor-mgr", InstanceID: "factor-mgr@control",
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
			CheckID: "placement:control:factor-mgr", Name: "factor-mgr@control",
			Source: domain.CheckSourcePlacement, Enabled: false, IntervalSeconds: 30,
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
	seedBusinessTopology(t, repositories, "control", "factor-mgr", false)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
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
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != noLongerExpected {
		t.Fatalf("results = %+v", results)
	}
}

func TestServiceDeploymentExpectedRequiresExplicitRegistration(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repositories := manager.Repositories()
	expected, err := serviceDeploymentExpected(t.Context(), repositories.Topology, "factor-mgr")
	require.NoError(t, err)
	require.False(t, expected, "unknown topology cannot invent a deployment")
	seedBusinessTopology(t, repositories, "control", "factor-mgr", true)
	expected, err = serviceDeploymentExpected(t.Context(), repositories.Topology, "factor-mgr")
	require.NoError(t, err)
	require.True(t, expected)
	seedBusinessTopology(t, repositories, "control", "factor-mgr", false)
	expected, err = serviceDeploymentExpected(t.Context(), repositories.Topology, "factor-mgr")
	require.NoError(t, err)
	require.False(t, expected)
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
			ServiceName: "collector", InstanceID: "collector@node-a", BootID: "boot-a",
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
				ServiceName: "collector", InstanceID: "collector@node-a", SeriesID: metric.id,
				MetricName: metric.name, MetricType: "gauge", LabelsJSON: metric.labels, LastSeenAt: staleAt,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&monmetrics.MetricLatest{
				SeriesID: metric.id, ServiceName: "collector", InstanceID: "collector@node-a",
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
	seedBusinessTopology(t, repositories, "node-a", "collector", true)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results,
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
	reporterCheckID := "reporter:node-a:collector"
	if len(checks) != 1 || checks[0].CheckID != reporterCheckID {
		t.Fatalf("checks = %+v", checks)
	}
	results, err := repositories.Results.Recent(t.Context(), monmetrics.InternalMetricSpaceID, reporterCheckID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Success || results[0].ErrorMessage != reporterReasonText("stale") {
		t.Fatalf("results = %+v", results)
	}

	if _, err := store.WithDatabase(manager, func(db *gorm.DB) struct{} {
		if err := db.Model(&monmetrics.MetricService{}).
			Where("c_service_name = ? AND c_instance_id = ?", "collector", "collector@node-a").
			Updates(map[string]any{"c_last_seen_at": now}).Error; err != nil {
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
	if len(results) != 1 || !results[0].Success || results[0].ErrorMessage != reporterReasonText("healthy") {
		t.Fatalf("recovery results = %+v", results)
	}
}

func buildBusinessFreshnessReporter(
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	hook func(context.Context, domain.Check, domain.CheckResult),
	klineEvaluators ...*monmetrics.KlineFreshnessEvaluator,
) func(context.Context) error {
	return buildBusinessFreshnessReporterWithInterval(builder, repositories, hook, 30*time.Second, klineEvaluators...)
}

func seedBusinessTopology(t *testing.T, repositories *store.Repositories, hostID, componentID string, enabled bool) {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	status := servicecatalog.Enabled
	if !enabled {
		status = servicecatalog.Disabled
	}
	_, err = repositories.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: time.Now().UTC(), Hosts: []domain.TopologyHost{{HostID: hostID, Address: "host.example.test", Status: servicecatalog.Enabled}}, Placements: []domain.TopologyPlacement{{HostID: hostID, ComponentID: componentID, Status: status}}}, nil)
	require.NoError(t, err)
}

// K 线新鲜度已覆盖的结果视图，不再重复生成 dataset 新鲜度检查（同一条数据链路只告警一次）；
// 同一主数据集上独立的视图不受影响，覆盖解除后恢复兜底。
func TestBusinessFreshnessReporterSkipsDatasetChecksCoveredByKlineFreshness(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	now := time.Now().UTC().Truncate(time.Second)
	viewLabels := func(view string) string {
		return `{"freq":"1m","space_id":"crypto","view_id":"` + view + `","dataset_id":"dataset_binance_kline_1m"}`
	}
	klineLabels := `{"space_id":"crypto","view_id":"view_binance_kline_1m","dataset_id":"dataset_binance_kline_1m","freq":"1m"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		series := []struct {
			id, name, labels string
			value            float64
		}{
			{"watermark-a", "moox_storage_view_output_watermark_timestamp_seconds", viewLabels("view_binance_kline_1m"), float64(now.Add(-time.Minute).Unix())},
			{"watermark-b", "moox_storage_view_output_watermark_timestamp_seconds", viewLabels("view_other_1m"), float64(now.Add(-time.Minute).Unix())},
			{"latest", monmetrics.ViewOutputLatestMetric, klineLabels, float64(now.Add(-time.Minute).Unix())},
			{"subjects", monmetrics.ViewOutputSubjectsMetric, klineLabels, 1},
		}
		for _, item := range series {
			require.NoError(t, db.Create(&monmetrics.MetricSeries{ServiceName: "storage-view", InstanceID: "storage-view@control", SeriesID: item.id, MetricName: item.name, MetricType: "gauge", LabelsJSON: item.labels, LastSeenAt: now}).Error)
			require.NoError(t, db.Create(&monmetrics.MetricLatest{SeriesID: item.id, ServiceName: "storage-view", InstanceID: "storage-view@control", MetricName: item.name, MetricType: "gauge", LabelsJSON: item.labels, Value: item.value, ObservedAt: now}).Error)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	repositories := manager.Repositories()
	clock := now
	build := func(withKline bool) func(context.Context) error {
		var evaluators []*monmetrics.KlineFreshnessEvaluator
		if withKline {
			rule := monmetrics.KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset_binance_kline_1m", ViewID: "view_binance_kline_1m", Frequency: "1m", MarketID: "crypto", StaleAfter: 5 * time.Minute}
			evaluators = append(evaluators, monmetrics.NewKlineFreshnessEvaluator(query, []monmetrics.KlineFreshnessRule{rule}, 20))
		}
		// K 线评估间隔 1 小时：同一个 reporter 的后续轮次不重新评估，验证覆盖集合会沿用。
		return buildBusinessFreshnessReporterWithInterval(&monitorobservability.Builder{
			Metrics: query, Checks: repositories.Checks, Topology: repositories.Topology, Results: repositories.Results, Now: func() time.Time { return clock },
		}, repositories, nil, time.Hour, evaluators...)
	}
	enabled := func(checkID string) bool {
		check, err := repositories.Checks.Get(t.Context(), "crypto", checkID)
		require.NoError(t, err, checkID)
		return check.Enabled
	}
	const viewA, viewB = "dataset:storage_view:view_binance_kline_1m:1m", "dataset:storage_view:view_other_1m:1m"

	// 没有 K 线新鲜度时，两个视图的 dataset 检查是唯一的新鲜度信号。
	require.NoError(t, build(false)(t.Context()))
	require.True(t, enabled(viewA))
	require.True(t, enabled(viewB))

	// K 线新鲜度覆盖视图 A 后：A 的检查被停用，同一主数据集上独立的视图 B 不受影响。
	covered := build(true)
	require.NoError(t, covered(t.Context()))
	require.False(t, enabled(viewA), "被 K 线新鲜度覆盖的视图检查必须停用")
	require.True(t, enabled(viewB), "同一主数据集上的独立视图仍要自己的检查")
	_, err = repositories.Checks.Get(t.Context(), "crypto", "kline_freshness:crypto:view_binance_kline_1m:1m")
	require.NoError(t, err)

	// 没到下次 K 线评估的轮次沿用覆盖集合，不会把 A 的检查重新生成。
	clock = clock.Add(30 * time.Second)
	require.NoError(t, covered(t.Context()))
	require.False(t, enabled(viewA))

	// 覆盖解除（这里用没有 K 线评估器的 reporter 代表评估关闭/失败）：兜底的 dataset 检查恢复启用。
	require.NoError(t, build(false)(t.Context()))
	require.True(t, enabled(viewA), "覆盖解除后必须恢复 dataset 检查兜底")
}

func TestKlineReportCoversDatasets(t *testing.T) {
	require.True(t, klineReportCoversDatasets(monmetrics.KlineFreshnessReport{CheckID: "kline_freshness:crypto:v:1m", Success: true}))
	require.True(t, klineReportCoversDatasets(monmetrics.KlineFreshnessReport{CheckID: "kline_freshness:stockcn:v:1m", Skipped: true, Reason: "market closed"}),
		"休市等正常跳过仍然覆盖，否则开闭市会让 dataset 检查反复生成又删除")
	require.False(t, klineReportCoversDatasets(monmetrics.KlineFreshnessReport{CheckID: "kline_freshness:crypto:v:1m", Skipped: true, Reason: "disabled"}))
	require.False(t, klineReportCoversDatasets(monmetrics.KlineFreshnessReport{CheckID: monmetrics.TaskResultInventoryCheckID}))
}

// 数据积累中的数据集检查按通过记录（不告警），其余非健康状态仍然失败。
func TestDatasetCheckPassesTreatsWarmingAsPassing(t *testing.T) {
	status := func(status, reason string) monitorobservability.DatasetFrequencyStatus {
		return monitorobservability.DatasetFrequencyStatus{Status: status, Reason: reason}
	}
	require.True(t, datasetCheckPasses(status("healthy", "normal")))
	require.True(t, datasetCheckPasses(status("degraded", monitorobservability.DatasetReasonWarming)))
	require.False(t, datasetCheckPasses(status("degraded", "尚无成功运行")))
	require.False(t, datasetCheckPasses(status("degraded", "success stale")))
	require.False(t, datasetCheckPasses(status("stale", "run stale")))
	require.False(t, datasetCheckPasses(status("unknown", "尚未上报")))
	require.Contains(t, datasetReasonText(status("degraded", monitorobservability.DatasetReasonWarming), time.Now()), "数据积累中")
}
