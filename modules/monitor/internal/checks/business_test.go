package checks

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/kline"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type businessFreshnessInventoryProvider struct {
	snapshot kline.TaskResultInventorySnapshot
	err      error
}

func (p *businessFreshnessInventoryProvider) FetchTaskResultInventory(context.Context) (kline.TaskResultInventorySnapshot, error) {
	if p.err != nil {
		return kline.TaskResultInventorySnapshot{}, p.err
	}
	return p.snapshot, nil
}

// openBusinessStore 打开一个载入了 schema 的空库。
func openBusinessStore(t *testing.T) (*store.Store, *store.Repositories) {
	t.Helper()
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	return manager, manager.Repositories()
}

// seedSeries 写一条指标序列与它的最新值。
func seedSeries(t *testing.T, db *gorm.DB, service, instance, id, name, labels string, value float64, seenAt time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&monmetrics.MetricSeries{
		ServiceName: service, InstanceID: instance, SeriesID: id,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, LastSeenAt: seenAt,
	}).Error)
	require.NoError(t, db.Clauses(metricLatestUpsert).Create(&monmetrics.MetricSeries{
		SeriesID: id, ServiceName: service, InstanceID: instance,
		MetricName: name, MetricType: "gauge", LabelsJSON: labels, Value: value, ObservedAt: seenAt,
	}).Error)
}

// seedCheckRow 预置一行失败中的检查；firing 为真时它正在告警。
func seedCheckRow(t *testing.T, repositories *store.Repositories, spaceID, checkID string, firing bool) {
	t.Helper()
	now := time.Now().UTC()
	row := domain.Check{
		SpaceID: spaceID, CheckID: checkID, Name: checkID, Severity: domain.SeverityCritical,
		Status: domain.CheckStatusDown, Reason: "旧失败", CheckedAt: now, FailureCount: 3,
		AlertStatus: domain.AlertStatusOK, CreatedAt: now,
	}
	if firing {
		row.AlertStatus, row.TriggeredAt, row.NotifiedAt = domain.AlertStatusFiring, &now, &now
	}
	require.NoError(t, repositories.Checks.SaveRound(t.Context(), []domain.Check{row}, nil, nil))
}

// checkRow 返回检查行；不存在时返回 nil。
func checkRow(t *testing.T, repositories *store.Repositories, spaceID, checkID string) *domain.Check {
	t.Helper()
	row, err := repositories.Checks.Get(t.Context(), spaceID, checkID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	require.NoError(t, err)
	return row
}

// alertEvents 返回某项检查的告警事件（最新在前）。
func alertEvents(t *testing.T, repositories *store.Repositories, checkID string) []domain.AlertEvent {
	t.Helper()
	all, err := repositories.Alerts.ListRecentEvents(t.Context(), 1000)
	require.NoError(t, err)
	var out []domain.AlertEvent
	for _, event := range all {
		if event.CheckID == checkID {
			out = append(out, event)
		}
	}
	return out
}

// requireRemovedWithRecovery 断言检查当轮被删除，并因为删除前在告警而写了一条“不再检查”的恢复事件。
func requireRemovedWithRecovery(t *testing.T, repositories *store.Repositories, spaceID, checkID string) {
	t.Helper()
	require.Nil(t, checkRow(t, repositories, spaceID, checkID), "不再产出的检查当轮删除")
	events := alertEvents(t, repositories, checkID)
	require.NotEmpty(t, events, checkID)
	require.Equal(t, domain.AlertEventResolved, events[0].EventType)
	require.Contains(t, events[0].Payload, ReasonRemoved)
}

func testApplier(repositories *store.Repositories) *Applier {
	return &Applier{Checks: repositories.Checks}
}

func buildBusinessFreshnessReporter(
	builder *monitorobservability.Builder,
	repositories *store.Repositories,
	klineEvaluators ...*kline.KlineFreshnessEvaluator,
) func(context.Context) error {
	return NewBusinessReporter(builder, repositories, testApplier(repositories), nil, 30*time.Second, klineEvaluators...)
}

// 对象不再产出（数据集删除）：在告警的检查写一条恢复事件并删行。
func TestBusinessFreshnessReporterRemovesDatasetNoLongerExpected(t *testing.T) {
	_, repositories := openBusinessStore(t)
	checkID := "dataset:collector:market_kline:1m"
	seedCheckRow(t, repositories, "crypto", checkID, true)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Topology: repositories.Topology}, repositories)
	require.NoError(t, run(t.Context()))
	requireRemovedWithRecovery(t, repositories, "crypto", checkID)
}

// 业务域不删探针域（行情探针、标签巡检）的检查。
func TestBusinessFreshnessReporterKeepsCanaryDomainChecks(t *testing.T) {
	_, repositories := openBusinessStore(t)
	for _, checkID := range []string{"market_canary:market_kline:BTC-USDT:1m:venue:binance", SubjectTagPrefix + "crypto:binance_spot"} {
		seedCheckRow(t, repositories, "crypto", checkID, true)
	}
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Topology: repositories.Topology}, repositories)
	require.NoError(t, run(t.Context()))
	for _, checkID := range []string{"market_canary:market_kline:BTC-USDT:1m:venue:binance", SubjectTagPrefix + "crypto:binance_spot"} {
		row := checkRow(t, repositories, "crypto", checkID)
		require.NotNil(t, row, checkID)
		require.True(t, row.Firing(), checkID)
		require.Empty(t, alertEvents(t, repositories, checkID), checkID)
	}
}

func klineViewQuery(t *testing.T, manager *store.Store, now time.Time) *monmetrics.QueryService {
	t.Helper()
	labels := `{"space_id":"crypto","dataset_id":"dataset","freq":"1m"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, row := range []monmetrics.MetricSeries{
			{SeriesID: "latest", MetricName: monmetrics.ViewOutputLatestMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-10 * time.Minute).Unix()), ObservedAt: now},
			{SeriesID: "subjects", MetricName: monmetrics.ViewOutputSubjectsMetric, MetricType: "gauge", LabelsJSON: labels, Value: 1, ObservedAt: now},
		} {
			require.NoError(t, db.Create(&row).Error)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	return query
}

func TestBusinessFreshnessReporterCreatesOneKlineGroupCheck(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	query := klineViewQuery(t, manager, now)
	evaluator := kline.NewKlineFreshnessEvaluator(query, []kline.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 5 * time.Minute,
	}}, 20)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now },
	}, repositories, evaluator)
	require.NoError(t, run(t.Context()))
	row := checkRow(t, repositories, "crypto", "kline_freshness:crypto:dataset:1m")
	require.NotNil(t, row)
	require.Equal(t, "dataset（1分钟）· K线结果", row.Name)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.True(t, strings.HasPrefix(row.Reason, "整体停止更新"), row.Reason)
	require.Equal(t, 1, row.FailureCount)
}

// 关闭 K 线新鲜度监控时 K 线检查不再产出、随之删除（在告警的推恢复）；重新开启后从零计数重新产出。
func TestBusinessFreshnessReporterRemovesKlineChecksWhenDisabledAndRecreates(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	query := klineViewQuery(t, manager, now)
	checkID := "kline_freshness:crypto:dataset:1m"
	seedCheckRow(t, repositories, "crypto", checkID, true)
	builder := &monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now }}
	require.NoError(t, buildBusinessFreshnessReporter(builder, repositories)(t.Context()))
	requireRemovedWithRecovery(t, repositories, "crypto", checkID)

	evaluator := kline.NewKlineFreshnessEvaluator(query, []kline.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 5 * time.Minute,
	}}, 20)
	require.NoError(t, buildBusinessFreshnessReporter(builder, repositories, evaluator)(t.Context()))
	row := checkRow(t, repositories, "crypto", checkID)
	require.NotNil(t, row, "当前任务清单仍期望的 K 线检查重新开启后照常产出")
	require.Equal(t, 1, row.FailureCount, "新建一行，计数从零开始")
	require.False(t, row.Firing())
}

// 没到 K 线评估时间的轮次：K 线检查保持原状态（冻结），不累计也不删除。
func TestBusinessFreshnessReporterReportsKlineFailureWithoutObservation(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	evaluator := kline.NewKlineFreshnessEvaluator(query, []kline.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 5 * time.Minute,
	}}, 20)
	run := NewBusinessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology}, repositories, testApplier(repositories), nil, 5*time.Minute, evaluator)
	require.NoError(t, run(t.Context()))
	require.NoError(t, run(t.Context()))
	row := checkRow(t, repositories, "crypto", "kline_freshness:crypto:dataset:1m")
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.True(t, strings.HasPrefix(row.Reason, "还没有收到任何结果数据"), row.Reason)
	require.Equal(t, 1, row.FailureCount, "没到评估时间的轮次不累计")
}

func TestBusinessFreshnessReporterPersistsInventoryFailureWithoutResolvingKlineCheck(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	provider := &businessFreshnessInventoryProvider{snapshot: kline.TaskResultInventorySnapshot{
		ID: "inventory-1", ObservedAt: now,
		Entries: []kline.TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a", Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now,
		}},
	}}
	cache, err := kline.NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	evaluator := kline.NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(2*time.Minute), 20)
	builder := &monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now }}
	run := NewBusinessReporter(builder, repositories, testApplier(repositories), nil, time.Second, evaluator)
	require.NoError(t, run(t.Context()))
	klineID := "kline_freshness:crypto:dataset-a:1m"
	row := checkRow(t, repositories, "crypto", klineID)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.True(t, strings.HasPrefix(row.Reason, "还没有收到任何结果数据"), row.Reason)

	now = now.Add(2 * time.Minute)
	provider.err = errors.New("collector inventory endpoint unavailable")
	require.NoError(t, run(t.Context()))
	// 采集服务不可用时沿用最后一份有效清单继续检查，同时单独告警“控制面数据过期”。
	inventory := checkRow(t, repositories, events.SystemSpaceID, kline.TaskResultInventoryCheckID)
	require.NotNil(t, inventory)
	require.Equal(t, domain.CheckStatusDown, inventory.Status)
	require.True(t, strings.HasPrefix(inventory.Reason, "控制面数据过期"), inventory.Reason)
	require.Equal(t, domain.SeverityWarning, inventory.Severity)
	// 沿用最后一份有效清单继续检查：已有的 K 线失败保持并继续累计，不会被合成为恢复。
	row = checkRow(t, repositories, "crypto", klineID)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.Equal(t, 2, row.FailureCount)
}

func TestBusinessFreshnessReporterDoesNotResolveKlineCheckDuringTaskPrepare(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	entry := kline.TaskResultInventoryEntry{
		SpaceID: "crypto", TaskID: "task-a", DatasetID: "dataset-a", Frequency: "1m",
		MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now,
	}
	provider := &businessFreshnessInventoryProvider{snapshot: kline.TaskResultInventorySnapshot{
		ID: "inventory-ready", ObservedAt: now, Entries: []kline.TaskResultInventoryEntry{entry},
	}}
	cache, err := kline.NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	evaluator := kline.NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(2*time.Minute), 20)
	builder := &monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now }}
	run := NewBusinessReporter(builder, repositories, testApplier(repositories), nil, time.Second, evaluator)
	require.NoError(t, run(t.Context()))
	klineID := "kline_freshness:crypto:dataset-a:1m"
	row := checkRow(t, repositories, "crypto", klineID)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusDown, row.Status)

	entry.OwnershipVerified = false
	entry.ResultStatus = "error"
	entry.ObservedAt = now.Add(2 * time.Minute)
	provider.snapshot = kline.TaskResultInventorySnapshot{ID: "inventory-error", ObservedAt: entry.ObservedAt, Entries: []kline.TaskResultInventoryEntry{entry}}
	now = entry.ObservedAt
	require.NoError(t, run(t.Context()))
	row = checkRow(t, repositories, "crypto", klineID)
	require.Equal(t, 2, row.FailureCount)
	require.True(t, strings.HasPrefix(row.Reason, "采集任务的结果状态异常"), "unowned task prepare error must remain a Kline failure: %+v", row)

	entry.ResultStatus = "pending"
	entry.ObservedAt = now.Add(2 * time.Minute)
	provider.snapshot = kline.TaskResultInventorySnapshot{ID: "inventory-pending", ObservedAt: entry.ObservedAt, Entries: []kline.TaskResultInventoryEntry{entry}}
	now = entry.ObservedAt
	require.NoError(t, run(t.Context()))
	row = checkRow(t, repositories, "crypto", klineID)
	require.NotNil(t, row, "准备中的任务本轮无法判断，检查保持原状态，不能删除")
	require.Equal(t, 2, row.FailureCount)
	require.Equal(t, domain.CheckStatusDown, row.Status, "pending preparation must not synthesize a Kline resolution")
}

// dataset: 检查只管非采集生产者（因子、策略等），不再为 collector 生成 dataset:collector: 检查。
func TestBusinessFreshnessReporterSkipsCollectorProducerDatasets(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"bars","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		seedSeries(t, db, "collector", "collector@node-a", "enabled", "moox_collector_dataset_enabled", labels, 1, now)
		seedSeries(t, db, "collector", "collector@node-a", "interval", "moox_collector_dataset_expected_interval_seconds", labels, 60, now)
		seedSeries(t, db, "collector", "collector@node-a", "inventory", "moox_collector_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now }}, repositories)
	require.NoError(t, run(t.Context()))
	require.Nil(t, checkRow(t, repositories, "crypto", "dataset:collector:bars:1m"))
}

func commitSeriesForTest(t *testing.T, messages *monmetrics.MetricMessageStore, eventID, service string, observed time.Time, names ...string) {
	t.Helper()
	samples := make([]monmetrics.Sample, 0, len(names))
	for _, name := range names {
		samples = append(samples, monmetrics.Sample{
			SeriesID: "series-" + name, ServiceName: service, InstanceID: service + "@node", MetricName: name,
			MetricType: "gauge", Value: 1, ObservedAt: observed, MessageID: eventID,
		})
	}
	report := &metricspb.MetricReport{ServiceName: service, InstanceId: service + "@node", BootId: "boot"}
	_, err := messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: eventID}, report, samples)
	require.NoError(t, err)
}

func TestBusinessFreshnessReporterChecksNewSeriesGrowth(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	messages.SetSeriesLimits(monmetrics.SeriesLimits{MaxNewPerHour: 10, MaxNewPerDay: 1000})
	now := time.Now().UTC().Truncate(time.Second)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: monmetrics.NewQueryService(messages, nil), Topology: repositories.Topology, Now: func() time.Time { return now },
	}, repositories)
	const checkID = "metrics_ingest:series_limit"
	latest := func() *domain.Check {
		t.Helper()
		require.NoError(t, run(t.Context()))
		return checkRow(t, repositories, events.SystemSpaceID, checkID)
	}

	commitSeriesForTest(t, messages, "e1", "noisy", now, "m1", "m2")
	row := latest()
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusOK, row.Status, "2/10 在限额内：%s", row.Reason)
	require.Contains(t, row.Reason, "2/10")

	// 9/10 达到 90%：还没丢弃，但已经接近上限。
	commitSeriesForTest(t, messages, "e2", "noisy", now, "m3", "m4", "m5", "m6", "m7", "m8", "m9")
	row = latest()
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.Contains(t, row.Reason, "指标序列新增过快")
	require.NotContains(t, row.Reason, "已丢弃")

	// 再来 3 个新序列：只放行 1 个，丢弃 2 个，原因里点名上报方。
	commitSeriesForTest(t, messages, "e3", "noisy", now, "m10", "m11", "m12")
	row = latest()
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.Contains(t, row.Reason, "已丢弃 noisy 的新序列 2 条")
	require.Equal(t, "指标序列数量保护", row.Name)
	require.Equal(t, domain.SeverityWarning, row.Severity)

	// 之后关闭限额：检查不再产出，当轮删除（在告警的推一条恢复），不会永久遗留。
	messages.SetSeriesLimits(monmetrics.SeriesLimits{})
	require.Nil(t, latest())
}

func TestBusinessFreshnessReporterSkipsSeriesLimitCheckWhenUnlimited(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: monmetrics.NewQueryService(messages, nil), Topology: repositories.Topology,
	}, repositories)
	require.NoError(t, run(t.Context()))
	require.Nil(t, checkRow(t, repositories, events.SystemSpaceID, "metrics_ingest:series_limit"))
}

// 部署停用后组件检查不再产出：在告警的推一条恢复并删行。
func TestBusinessFreshnessReporterRemovesReporterForDisabledDeployment(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		require.NoError(t, db.Create(&monmetrics.MetricService{
			ServiceName: "factor-mgr", InstanceID: "factor-mgr@control", BootID: "old-boot",
			NodeID: "control", LastSeenAt: now.Add(-time.Hour), IsStale: true,
		}).Error)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	checkID := "reporter:control:factor-mgr"
	seedCheckRow(t, repositories, events.SystemSpaceID, checkID, true)
	seedBusinessTopology(t, repositories, "control", "factor-mgr", false)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology}, repositories)
	require.NoError(t, run(t.Context()))
	requireRemovedWithRecovery(t, repositories, events.SystemSpaceID, checkID)
}

// 生产者没有部署（factor-mgr 停用）时它的数据集检查不再产出：在告警的推一条恢复并删行。
func TestBusinessFreshnessReporterRemovesDatasetForDisabledProducer(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"factor_output","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "factor-enabled", "moox_factor_dataset_enabled", labels, 1, now.Add(-time.Hour))
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	checkID := "dataset:factor:factor_output:1m"
	seedCheckRow(t, repositories, "crypto", checkID, true)
	seedBusinessTopology(t, repositories, "control", "factor-mgr", false)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology}, repositories)
	require.NoError(t, run(t.Context()))
	requireRemovedWithRecovery(t, repositories, "crypto", checkID)
}

func TestServiceDeploymentExpectedRequiresExplicitRegistration(t *testing.T) {
	_, repositories := openBusinessStore(t)
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

// staleFactorQuery 是 node-a 上的 factor-mgr 10 分钟前停止上报：服务行与它的数据集序列都过期。
func staleFactorQuery(t *testing.T, manager *store.Store, now time.Time) *monmetrics.QueryService {
	t.Helper()
	staleAt := now.Add(-10 * time.Minute)
	labels := `{"dataset_id":"factor_output","freq":"1m","space_id":"crypto"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		require.NoError(t, db.Create(&monmetrics.MetricService{
			ServiceName: "factor-mgr", InstanceID: "factor-mgr@node-a", BootID: "boot-a", NodeID: "node-a", LastSeenAt: staleAt,
		}).Error)
		for _, metric := range []struct {
			id, name, labels string
			value            float64
		}{
			{"enabled", "moox_factor_dataset_enabled", labels, 1},
			{"interval", "moox_factor_dataset_expected_interval_seconds", labels, 60},
			{"inventory", "moox_factor_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix())},
			{"run", "moox_factor_dataset_last_run_timestamp_seconds", labels, float64(now.Unix())},
			{"success", "moox_factor_dataset_last_success_timestamp_seconds", labels, float64(now.Unix())},
			{"output", "moox_factor_dataset_output_watermark_timestamp_seconds", labels, float64(now.Unix())},
		} {
			seedSeries(t, db, "factor-mgr", "factor-mgr@node-a", metric.id, metric.name, metric.labels, metric.value, staleAt)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	return query
}

var stalePolicy = report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
	RunMissedIntervals: 2, SuccessMissedIntervals: 3, MinuteLateness: 10 * time.Minute, HourLateness: time.Hour,
}}

func TestBusinessFreshnessReporterAlertsOncePerStaleReporterAndSuppressesDatasets(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	query := staleFactorQuery(t, manager, now)
	seedBusinessTopology(t, repositories, "node-a", "factor-mgr", true)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Policy: stalePolicy}, repositories)
	require.NoError(t, run(t.Context()))
	rows, err := repositories.Checks.List(t.Context(), "")
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.CheckID)
	}
	slices.Sort(ids)
	require.Equal(t, []string{"reporter:node-a:factor-mgr", "restart:node-a:factor-mgr"}, ids, "只有停止上报与反复重启两条组件检查，数据集不重复告警")
	reporterCheckID := "reporter:node-a:factor-mgr"
	row := checkRow(t, repositories, events.SystemSpaceID, reporterCheckID)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.Equal(t, reporterReasonText("stale"), row.Reason)

	_, err = store.WithDatabase(manager, func(db *gorm.DB) struct{} {
		require.NoError(t, db.Model(&monmetrics.MetricService{}).
			Where("c_service_name = ? AND c_instance_id = ?", "factor-mgr", "factor-mgr@node-a").
			Updates(map[string]any{"c_last_seen_at": now}).Error)
		return struct{}{}
	})
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	row = checkRow(t, repositories, events.SystemSpaceID, reporterCheckID)
	require.Equal(t, domain.CheckStatusOK, row.Status)
	require.Equal(t, reporterReasonText("healthy"), row.Reason)
}

// 上报方停止上报（本轮有它的 reporter: 失败事实）时，它的关注项与 producer stale 的数据集检查保持原状态，不推假恢复；
// 部署停用后没有 reporter: 检查，这些检查随之删除。
func TestBusinessFreshnessReporterFreezesChecksOfSilentReporter(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	query := staleFactorQuery(t, manager, now)
	seedBusinessTopology(t, repositories, "node-a", "factor-mgr", true)
	attention := monitorobservability.AttentionCheckID("factor-mgr", "engine_offline")
	dataset := "dataset:factor:factor_output:1m"
	seedCheckRow(t, repositories, events.SystemSpaceID, attention, true)
	seedCheckRow(t, repositories, "crypto", dataset, true)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Policy: stalePolicy}, repositories)
	for range 2 {
		require.NoError(t, run(t.Context()))
	}
	for _, key := range []store.CheckKey{{SpaceID: events.SystemSpaceID, CheckID: attention}, {SpaceID: "crypto", CheckID: dataset}} {
		row := checkRow(t, repositories, key.SpaceID, key.CheckID)
		require.NotNil(t, row, key.CheckID)
		require.True(t, row.Firing(), key.CheckID)
		require.Equal(t, 3, row.FailureCount, "冻结的行不推进: %s", key.CheckID)
		require.Empty(t, alertEvents(t, repositories, key.CheckID), "上报方停报时不推假恢复: %s", key.CheckID)
	}

	seedBusinessTopology(t, repositories, "node-a", "factor-mgr", false)
	require.NoError(t, run(t.Context()))
	requireRemovedWithRecovery(t, repositories, events.SystemSpaceID, attention)
	requireRemovedWithRecovery(t, repositories, "crypto", dataset)
}

// 生产者刚停报的首轮：服务已判停止上报，关注项序列还在多放的一轮之内（2 分 10 秒）或刚过期（2 分 40 秒），都不推恢复。
func TestBusinessFreshnessReporterDoesNotResolveAttentionWhenProducerJustStopped(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		require.NoError(t, db.Create(&monmetrics.MetricService{
			ServiceName: "trade", InstanceID: "trade@node-a", BootID: "boot-a", NodeID: "node-a", LastSeenAt: now.Add(-130 * time.Second),
		}).Error)
		seedSeries(t, db, "trade", "trade@node-a", "paused", report.AttentionMetric,
			`{"kind":"auto_paused","name":"组合账户自动暂停","severity":"critical","detail":"组合 A"}`, 1, now.Add(-130*time.Second))
		seedSeries(t, db, "trade", "trade@node-a", "blocked", report.AttentionMetric,
			`{"kind":"target_blocked","name":"交易目标持续阻塞","severity":"critical","detail":"组合 A"}`, 1, now.Add(-160*time.Second))
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	seedBusinessTopology(t, repositories, "node-a", "trade", true)
	paused := monitorobservability.AttentionCheckID("trade", "auto_paused")
	blocked := monitorobservability.AttentionCheckID("trade", "target_blocked")
	seedCheckRow(t, repositories, events.SystemSpaceID, paused, true)
	seedCheckRow(t, repositories, events.SystemSpaceID, blocked, true)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now }}, repositories)
	require.NoError(t, run(t.Context()))
	reporter := checkRow(t, repositories, events.SystemSpaceID, "reporter:node-a:trade")
	require.NotNil(t, reporter)
	require.Equal(t, domain.CheckStatusDown, reporter.Status, "服务已判停止上报")
	for _, checkID := range []string{paused, blocked} {
		row := checkRow(t, repositories, events.SystemSpaceID, checkID)
		require.NotNil(t, row, checkID)
		require.True(t, row.Firing(), checkID)
		for _, event := range alertEvents(t, repositories, checkID) {
			require.NotEqual(t, domain.AlertEventResolved, event.EventType, "生产者刚停报时不推恢复: %s", checkID)
		}
	}
}

func seedBusinessTopology(t *testing.T, repositories *store.Repositories, hostID, componentID string, enabled bool) {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	status := servicecatalog.Enabled
	if !enabled {
		status = servicecatalog.Disabled
	}
	err = repositories.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: time.Now().UTC(), Hosts: []domain.TopologyHost{{HostID: hostID, Address: "host.example.test", Status: servicecatalog.Enabled}}, Deployments: []domain.TopologyDeployment{{HostID: hostID, ComponentID: componentID, Status: status}}})
	require.NoError(t, err)
}

// K 线类采集数据集只由 K 线新鲜度出检查，storage / storage_view 按采集任务清单区分采集数据集与因子等非采集数据集；
// 任务清单从未取到时冻结 dataset:storage: 与 dataset:storage_view:，不误建采集数据集检查也不误删已有因子检查。
func TestBusinessFreshnessReporterSplitsCollectorAndNonCollectorDatasetsByInventory(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	seedBusinessTopology(t, repositories, "control", "factor-mgr", true)
	labelsOf := func(dataset string) string {
		return `{"dataset_id":"` + dataset + `","freq":"1m","space_id":"crypto"}`
	}
	const (
		klineDataset  = "dataset_binance_kline_1m"
		factorDataset = "dataset_factor_ema_1m"
	)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		// 采集与因子两条数据集同时有 storage、storage_view 与各自生产者指标。
		seedSeries(t, db, "collector", "collector@control", "col-en", "moox_collector_dataset_enabled", labelsOf(klineDataset), 1, now)
		seedSeries(t, db, "collector", "collector@control", "col-iv", "moox_collector_dataset_expected_interval_seconds", labelsOf(klineDataset), 60, now)
		seedSeries(t, db, "storage-primary", "storage-primary@control", "st-col-run", "moox_storage_dataset_last_run_timestamp_seconds", labelsOf(klineDataset), float64(now.Unix()), now)
		seedSeries(t, db, "storage-primary", "storage-primary@control", "st-col-ok", "moox_storage_dataset_last_success_timestamp_seconds", labelsOf(klineDataset), float64(now.Unix()), now)
		seedSeries(t, db, "storage-primary", "storage-primary@control", "st-col-out", "moox_storage_dataset_output_watermark_timestamp_seconds", labelsOf(klineDataset), float64(now.Add(-time.Minute).Unix()), now)
		seedSeries(t, db, "storage-view", "storage-view@control", "vw-col", "moox_storage_view_output_watermark_timestamp_seconds", labelsOf(klineDataset), float64(now.Add(-time.Minute).Unix()), now)

		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "fac-en", "moox_factor_dataset_enabled", labelsOf(factorDataset), 1, now)
		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "fac-iv", "moox_factor_dataset_expected_interval_seconds", labelsOf(factorDataset), 60, now)
		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "fac-inv", "moox_factor_dataset_inventory_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "fac-run", "moox_factor_dataset_last_run_timestamp_seconds", labelsOf(factorDataset), float64(now.Unix()), now)
		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "fac-ok", "moox_factor_dataset_last_success_timestamp_seconds", labelsOf(factorDataset), float64(now.Unix()), now)
		seedSeries(t, db, "factor-mgr", "factor-mgr@control", "fac-out", "moox_factor_dataset_output_watermark_timestamp_seconds", labelsOf(factorDataset), float64(now.Add(-time.Minute).Unix()), now)
		seedSeries(t, db, "storage-primary", "storage-primary@control", "st-fac-run", "moox_storage_dataset_last_run_timestamp_seconds", labelsOf(factorDataset), float64(now.Unix()), now)
		seedSeries(t, db, "storage-primary", "storage-primary@control", "st-fac-ok", "moox_storage_dataset_last_success_timestamp_seconds", labelsOf(factorDataset), float64(now.Unix()), now)
		seedSeries(t, db, "storage-primary", "storage-primary@control", "st-fac-out", "moox_storage_dataset_output_watermark_timestamp_seconds", labelsOf(factorDataset), float64(now.Add(-time.Minute).Unix()), now)
		seedSeries(t, db, "storage-view", "storage-view@control", "vw-fac", "moox_storage_view_output_watermark_timestamp_seconds", labelsOf(factorDataset), float64(now.Add(-time.Minute).Unix()), now)

		seedSeries(t, db, "storage-view", "storage-view@control", "latest", monmetrics.ViewOutputLatestMetric, labelsOf(klineDataset), float64(now.Add(-time.Minute).Unix()), now)
		seedSeries(t, db, "storage-view", "storage-view@control", "subjects", monmetrics.ViewOutputSubjectsMetric, labelsOf(klineDataset), 1, now)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)

	provider := &businessFreshnessInventoryProvider{snapshot: kline.TaskResultInventorySnapshot{
		ID: "inv-1", ObservedAt: now,
		Entries: []kline.TaskResultInventoryEntry{{
			SpaceID: "crypto", TaskID: "t1", DatasetID: klineDataset, Frequency: "1m",
			MarketID: "crypto", Enabled: true, OwnershipVerified: true, ResultStatus: "ready", ObservedAt: now,
		}},
	}}
	cache, err := kline.NewTaskResultInventoryCache(provider, time.Minute, 10)
	require.NoError(t, err)
	clock := now
	evaluator := kline.NewKlineFreshnessEvaluatorWithInventory(query, cache, latenessPolicy(5*time.Minute), 20)
	run := NewBusinessReporter(&monitorobservability.Builder{
		Metrics: query, Topology: repositories.Topology, Inventory: cache, Policy: latenessPolicy(5 * time.Minute),
		Now: func() time.Time { return clock },
	}, repositories, testApplier(repositories), nil, time.Hour, evaluator)

	exists := func(checkID string) bool { return checkRow(t, repositories, "crypto", checkID) != nil }

	// 第一轮：采集数据集只产出 kline_freshness:，不产出 dataset:collector/storage/storage_view；因子数据集全部产出。
	require.NoError(t, run(t.Context()))
	require.True(t, exists("kline_freshness:crypto:"+klineDataset+":1m"))
	require.False(t, exists("dataset:collector:"+klineDataset+":1m"))
	require.False(t, exists("dataset:storage:"+klineDataset+":1m"))
	require.False(t, exists("dataset:storage_view:"+klineDataset+":1m"))
	require.True(t, exists("dataset:factor:"+factorDataset+":1m"))
	require.True(t, exists("dataset:storage:"+factorDataset+":1m"))
	require.True(t, exists("dataset:storage_view:"+factorDataset+":1m"))

	// 没到 K 线评估间隔的下一轮：K 线冻结，采集数据集依然不出 dataset: 检查，因子数据集照常存在。
	clock = clock.Add(30 * time.Second)
	require.NoError(t, run(t.Context()))
	require.True(t, exists("kline_freshness:crypto:"+klineDataset+":1m"))
	require.False(t, exists("dataset:storage:"+klineDataset+":1m"))
	require.False(t, exists("dataset:storage_view:"+klineDataset+":1m"))
	require.True(t, exists("dataset:storage:"+factorDataset+":1m"))
	require.True(t, exists("dataset:storage_view:"+factorDataset+":1m"))

	// 从未取到清单时：冻结 dataset:storage: 与 dataset:storage_view:，既不误删已有因子检查，也不给采集数据集建重复检查。
	brokenCache, err := kline.NewTaskResultInventoryCache(&businessFreshnessInventoryProvider{err: errors.New("collector down")}, time.Minute, 10)
	require.NoError(t, err)
	brokenRun := NewBusinessReporter(&monitorobservability.Builder{
		Metrics: query, Topology: repositories.Topology, Inventory: brokenCache, Policy: latenessPolicy(5 * time.Minute),
		Now: func() time.Time { return clock },
	}, repositories, testApplier(repositories), nil, time.Second, kline.NewKlineFreshnessEvaluatorWithInventory(query, brokenCache, latenessPolicy(5*time.Minute), 20))
	require.NoError(t, brokenRun(t.Context()))
	require.False(t, exists("dataset:storage:"+klineDataset+":1m"))
	require.False(t, exists("dataset:storage_view:"+klineDataset+":1m"))
	require.True(t, exists("dataset:storage:"+factorDataset+":1m"), "清单不可用时冻结已有的因子 storage 检查，不误删")
	require.True(t, exists("dataset:storage_view:"+factorDataset+":1m"), "清单不可用时冻结已有的因子 storage_view 检查，不误删")
	require.True(t, exists("dataset:factor:"+factorDataset+":1m"), "因子生产者检查不依赖清单，照常推进")
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

// 策略数据集检查用策略随清单上报的实例名命名：刚改名、旧名称序列还没过期时取最近上报的那条；只剩已过期的名称序列
// （策略不再上报名称）时退回数据集 ID，不报错。
func TestBusinessFreshnessReporterNamesStrategyDatasetByInstance(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	named := `{"dataset_id":"strategy_26d51f7e-4c40","freq":"1m","space_id":"crypto"}`
	unnamed := `{"dataset_id":"strategy_b","freq":"1m","space_id":"crypto"}`
	info := func(datasetID, name string) string {
		return `{"dataset_id":"` + datasetID + `","freq":"1m","name":"` + name + `","space_id":"crypto"}`
	}
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, metric := range []struct {
			id, name, labels string
			value            float64
			seenAt           time.Time
		}{
			{"enabled-a", "moox_strategy_dataset_enabled", named, 1, now},
			{"interval-a", "moox_strategy_dataset_expected_interval_seconds", named, 60, now},
			{"enabled-b", "moox_strategy_dataset_enabled", unnamed, 1, now},
			{"interval-b", "moox_strategy_dataset_expected_interval_seconds", unnamed, 60, now},
			{"info-a-current", "moox_strategy_dataset_info", info("strategy_26d51f7e-4c40", "动量轮动（26d51f7e）"), 1, now},
			{"info-a-renamed", "moox_strategy_dataset_info", info("strategy_26d51f7e-4c40", "旧名称（26d51f7e）"), 1, now.Add(-30 * time.Second)},
			{"info-b-stale", "moox_strategy_dataset_info", info("strategy_b", "已停用名称（strategy）"), 1, now.Add(-10 * time.Minute)},
		} {
			seedSeries(t, db, "strategy", "strategy@node-a", metric.id, metric.name, metric.labels, metric.value, metric.seenAt)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now }}, repositories)
	require.NoError(t, run(t.Context()))
	require.Equal(t, "动量轮动（26d51f7e）· 策略结果", checkRow(t, repositories, "crypto", "dataset:strategy:strategy_26d51f7e-4c40:1m").Name)
	require.Equal(t, "strategy_b（1分钟）· 策略结果", checkRow(t, repositories, "crypto", "dataset:strategy:strategy_b:1m").Name)
}

// latenessPolicy 是所有周期都允许迟到 lateness 的健康策略。
func latenessPolicy(lateness time.Duration) report.RealtimeTimeSeriesPolicy {
	return report.RealtimeTimeSeriesPolicy{Defaults: report.RealtimeTimeSeriesDefaults{
		RunMissedIntervals: 2, SuccessMissedIntervals: 3, MinuteLateness: lateness, HourLateness: lateness,
	}}
}

// 观测输入（Monitor 的 EventBus 观测消费者）中断期间：依赖服务指标的检查冻结，既不新增"已停止上报"告警，
// 也只留一条"观测输入"检查说明原因；刚恢复的宽限期内同样冻结，宽限结束后恢复判定。
func TestBusinessFreshnessReporterFreezesObservationChecksWhileIngestIsDown(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		require.NoError(t, db.Create(&monmetrics.MetricService{
			ServiceName: "collector", InstanceID: "collector@node-a", BootID: "boot-a", NodeID: "node-a", LastSeenAt: now.Add(-10 * time.Minute),
		}).Error)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	seedBusinessTopology(t, repositories, "node-a", "collector", true)
	// 观测输入中断前就有的旧检查：冻结期间不删除。
	seedCheckRow(t, repositories, "crypto", "dataset:collector:old:1m", true)

	state := ObservationState{Suspended: true, Reason: "eventbus unreachable"}
	run := NewBusinessReporter(&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology},
		repositories, testApplier(repositories), func(time.Time) ObservationState { return state }, 30*time.Second)
	const reporterCheck = "reporter:node-a:collector"

	// 观测消费者没连上：上报方已经 10 分钟没样本，但这是因为输入断了，不判定；"观测输入"检查失败并带上原因。
	require.NoError(t, run(t.Context()))
	require.Nil(t, checkRow(t, repositories, events.SystemSpaceID, reporterCheck), "观测输入中断期间不能判定上报方停止上报")
	ingest := checkRow(t, repositories, events.SystemSpaceID, ObservationIngestCheckID)
	require.NotNil(t, ingest)
	require.Equal(t, domain.CheckStatusDown, ingest.Status)
	require.Contains(t, ingest.Reason, "观测输入中断")
	require.Equal(t, "eventbus unreachable", ingest.RawError)
	require.NotNil(t, checkRow(t, repositories, "crypto", "dataset:collector:old:1m"), "冻结期间旧检查不删除")

	// 刚重新连上、还在宽限期内：输入已恢复（观测输入检查通过），超时判定继续冻结，等上报方补上样本。
	state = ObservationState{Suspended: true, Ready: true}
	require.NoError(t, run(t.Context()))
	require.Nil(t, checkRow(t, repositories, events.SystemSpaceID, reporterCheck), "恢复宽限期内仍然冻结")
	require.Equal(t, domain.CheckStatusOK, checkRow(t, repositories, events.SystemSpaceID, ObservationIngestCheckID).Status)

	// 宽限结束，上报方仍然没有样本：这时才是真正的"已停止上报"。
	state = ObservationState{Ready: true}
	require.NoError(t, run(t.Context()))
	row := checkRow(t, repositories, events.SystemSpaceID, reporterCheck)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.Equal(t, reporterReasonText("stale"), row.Reason)
}

// V3 复现：K 线新鲜度的视图摘要依赖经 EventBus 入库的 storage-view 指标，快照超过 1 分钟后判逾期会退回当前时间；
// 观测输入断开或处于恢复宽限期时，K 线新鲜度必须一并冻结，不能把未收到新快照误报成“整体停止更新”。
func TestBusinessFreshnessReporterFreezesKlineChecksWhileIngestIsDown(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Date(2026, 9, 8, 10, 0, 30, 0, time.UTC)
	labels := `{"space_id":"crypto","dataset_id":"dataset","freq":"1m"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		for _, row := range []monmetrics.MetricSeries{
			// 10:00:30 时已收盘的最新一根是 09:59:00，快照刚刚上报，处于正常状态。
			{SeriesID: "kline-latest", MetricName: monmetrics.ViewOutputLatestMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-90 * time.Second).Unix()), ObservedAt: now},
			{SeriesID: "kline-subjects", MetricName: monmetrics.ViewOutputSubjectsMetric, MetricType: "gauge", LabelsJSON: labels, Value: 1, ObservedAt: now},
		} {
			require.NoError(t, db.Create(&row).Error)
		}
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)

	evaluator := kline.NewKlineFreshnessEvaluator(query, []kline.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute,
	}}, 20)
	currentNow := now
	state := ObservationState{Ready: true}
	run := NewBusinessReporter(
		&monitorobservability.Builder{Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return currentNow }},
		repositories,
		testApplier(repositories),
		func(time.Time) ObservationState { return state },
		30*time.Second,
		evaluator,
	)
	const klineCheckID = "kline_freshness:crypto:dataset:1m"

	// 初始观测正常：K 线检查通过。
	require.NoError(t, run(t.Context()))
	row := checkRow(t, repositories, "crypto", klineCheckID)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusOK, row.Status)
	require.Zero(t, row.FailureCount)

	// 5 分钟后观测输入断开：库里的 storage-view 摘要仍停在 t0（距今超过 1 分钟快照宽限，按当前时间已超过 2 分钟允许迟到），
	// 但此时观测输入中断，K 线检查必须保持冻结，不得误报为“整体停止更新”。
	currentNow = now.Add(5 * time.Minute)
	state = ObservationState{Suspended: true, Reason: "eventbus unreachable"}
	require.NoError(t, run(t.Context()))
	row = checkRow(t, repositories, "crypto", klineCheckID)
	require.NotNil(t, row, "观测输入中断时冻结 K 线检查，不得删除")
	require.Equal(t, domain.CheckStatusOK, row.Status, "观测输入中断时 K 线检查不得误报停止更新")
	require.Zero(t, row.FailureCount)
	ingest := checkRow(t, repositories, events.SystemSpaceID, ObservationIngestCheckID)
	require.NotNil(t, ingest)
	require.Equal(t, domain.CheckStatusDown, ingest.Status)

	// 观测刚重连，仍在恢复宽限期内：继续冻结。
	currentNow = now.Add(6 * time.Minute)
	state = ObservationState{Suspended: true, Ready: true}
	require.NoError(t, run(t.Context()))
	row = checkRow(t, repositories, "crypto", klineCheckID)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusOK, row.Status, "恢复宽限期内 K 线检查继续冻结")
	require.Zero(t, row.FailureCount)
	require.Equal(t, domain.CheckStatusOK, checkRow(t, repositories, events.SystemSpaceID, ObservationIngestCheckID).Status)

	// 宽限期结束后快照依然落后：此时恢复判定并报出停止更新。
	currentNow = now.Add(7 * time.Minute)
	state = ObservationState{Ready: true}
	require.NoError(t, run(t.Context()))
	row = checkRow(t, repositories, "crypto", klineCheckID)
	require.NotNil(t, row)
	require.Equal(t, domain.CheckStatusDown, row.Status)
	require.True(t, strings.HasPrefix(row.Reason, "整体停止更新"), row.Reason)
	require.Equal(t, 1, row.FailureCount)
}

func TestObservationIndependentChecksKeepRunning(t *testing.T) {
	require.True(t, ObservationIndependent(ObservationIngestCheckID), ObservationIngestCheckID)
	for _, id := range []string{
		"kline_freshness:crypto:1m",
		"kline_freshness:task_result_inventory",
		"reporter:node-a:collector",
		"ready:node-a:collector",
		"restart:node-a:collector",
		"dataset:storage:ds:1m",
		"balance:trade",
		"data_delivery:storage",
		"attention:trade:auto_paused",
	} {
		require.False(t, ObservationIndependent(id), id)
	}
}

// 策略实例因上游历史补齐暂停（enabled=-1）：不生成数据集新鲜度检查；原来在告警的检查本轮没有事实，按“不再检查”删除并推恢复。
func TestBusinessFreshnessReporterSkipsDatasetCheckWhileSeeding(t *testing.T) {
	manager, repositories := openBusinessStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	labels := `{"dataset_id":"strategy_seed","freq":"5m","space_id":"stockus"}`
	query, err := store.WithDatabase(manager, func(db *gorm.DB) *monmetrics.QueryService {
		seedSeries(t, db, "strategy", "strategy@node-a", "enabled-seed", "moox_strategy_dataset_enabled", labels, -1, now)
		return monmetrics.NewQueryService(monmetrics.NewMetricMessageStore(db), nil)
	})
	require.NoError(t, err)
	checkID := "dataset:strategy:strategy_seed:5m"
	seedCheckRow(t, repositories, "stockus", checkID, true)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: query, Topology: repositories.Topology, Now: func() time.Time { return now },
	}, repositories)
	require.NoError(t, run(t.Context()))
	requireRemovedWithRecovery(t, repositories, "stockus", checkID)
}

func TestReporterExpectationUsesStableDeploymentIdentity(t *testing.T) {
	manager, repos := openBusinessStore(t)
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	query := monmetrics.NewQueryService(messages, nil)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	now := time.Now().UTC()
	snapshot := domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts:       []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Deployments: []domain.TopologyDeployment{{HostID: "control", ComponentID: "factor-mgr", Status: "enabled"}},
	}
	err = repos.Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	run := buildBusinessFreshnessReporter(&monitorobservability.Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}, repos)
	require.NoError(t, run(t.Context()))
	const id = "reporter:control:factor-mgr"
	result, ok := latestResult(t, repos, "mooxsys", id)
	require.True(t, ok)
	require.False(t, result.Success, "登记了部署就要求上报：从没上报过按停止上报处理")
	_, err = messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: "factor-mgr-1", OccurredAt: now.UnixMilli()}, &metricspb.MetricReport{ServiceName: "factor-mgr", NodeId: "control", InstanceId: "factor-mgr@control", BootId: "boot-1"}, nil)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	result, _ = latestResult(t, repos, "mooxsys", id)
	require.True(t, result.Success, "the first report resolves the same check identity")
	rows, err := repos.Checks.List(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, rows, 2, "停止上报一条、反复重启一条；上报里没有就绪判断时不生成未就绪检查")
	snapshot.Deployments[0].Status = "disabled"
	err = repos.Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	require.Nil(t, checkRow(t, repos, "mooxsys", id), "停用部署后组件检查不再产出，当轮删除")
	require.Empty(t, alertEvents(t, repos, id), "没在告警的检查删除时不写恢复事件")
	snapshot.Deployments[0].Status = "enabled"
	err = repos.Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	require.NotNil(t, checkRow(t, repos, "mooxsys", id), "重新启用的部署恢复组件检查")
	snapshot.Deployments = nil
	err = repos.Topology.Reconcile(t.Context(), snapshot)
	require.NoError(t, err)
	require.NoError(t, run(t.Context()))
	require.Nil(t, checkRow(t, repos, "mooxsys", id), "删除部署后组件检查删除")
}

// Collector 模块暂停或恢复追赶时采集是有意停下的：K 线结果检查不评估（不新增失败结果），放行后恢复评估。
func TestBusinessFreshnessSkipsKlineChecksWhileCollectorHalted(t *testing.T) {
	manager, repos := openBusinessStore(t)
	messages, err := store.WithDatabase(manager, monmetrics.NewMetricMessageStore)
	require.NoError(t, err)
	now := time.Now().UTC()
	labels := `{"space_id":"crypto","dataset_id":"dataset","freq":"1m"}`
	_, err = store.WithDatabase(manager, func(db *gorm.DB) error {
		for _, row := range []monmetrics.MetricSeries{
			{SeriesID: "latest", MetricName: monmetrics.ViewOutputLatestMetric, MetricType: "gauge", LabelsJSON: labels, Value: float64(now.Add(-10 * time.Minute).Unix()), ObservedAt: now},
			{SeriesID: "subjects", MetricName: monmetrics.ViewOutputSubjectsMetric, MetricType: "gauge", LabelsJSON: labels, Value: 1, ObservedAt: now},
		} {
			require.NoError(t, db.Create(&row).Error)
		}
		return nil
	})
	require.NoError(t, err)
	query := monmetrics.NewQueryService(messages, nil)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, repos.Topology.Reconcile(t.Context(), domain.TopologySnapshot{Catalog: catalog, ObservedAt: now,
		Hosts:       []domain.TopologyHost{{HostID: "control", Address: "control.test", Status: "enabled"}},
		Deployments: []domain.TopologyDeployment{{HostID: "control", ComponentID: "collector", Status: "enabled"}},
	}))
	report := func(stage string) {
		_, err := messages.CommitIngest(t.Context(), &eventpb.EventMessage{EventId: "collector-" + stage, OccurredAt: now.UnixMilli()},
			&metricspb.MetricReport{ServiceName: "collector", NodeId: "control", InstanceId: "collector@control", BootId: "boot-1",
				Lifecycle: &metricspb.ModuleLifecycle{Stage: stage}}, nil)
		require.NoError(t, err)
	}
	evaluator := kline.NewKlineFreshnessEvaluator(query, []kline.KlineFreshnessRule{{
		Enabled: true, SpaceID: "crypto", DatasetID: "dataset", Frequency: "1m", MarketID: "crypto", Lateness: 5 * time.Minute,
	}}, 20)
	builder := &monitorobservability.Builder{Metrics: query, Topology: repos.Topology, Now: func() time.Time { return now }}
	run := buildBusinessFreshnessReporter(builder, repos, evaluator)
	const id = "kline_freshness:crypto:dataset:1m"

	for _, stage := range []string{"paused", "resuming"} {
		report(stage)
		services, err := builder.Services(t.Context(), now)
		require.NoError(t, err)
		require.True(t, CollectorHalted(services), stage)
		require.NoError(t, run(t.Context()))
		_, judged := latestResult(t, repos, "crypto", id)
		require.False(t, judged, "Collector %s 时不评估 K 线结果", stage)
	}

	report("running")
	services, err := builder.Services(t.Context(), now)
	require.NoError(t, err)
	require.False(t, CollectorHalted(services))
	require.NoError(t, run(t.Context()))
	result, judged := latestResult(t, repos, "crypto", id)
	require.True(t, judged)
	require.False(t, result.Success, "放行后照常判定")
}
