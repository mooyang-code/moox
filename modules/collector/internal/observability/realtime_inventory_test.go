package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

type ruleSourceStub struct {
	rules []domain.CollectionTask
	err   error
}

func (s *ruleSourceStub) ListEnabledAll(context.Context, int) ([]domain.CollectionTask, error) {
	return s.rules, s.err
}

type registryStub struct {
	items  []report.DatasetExpectation
	runs   []report.DatasetObservation
	errors int
}

func (s *registryStub) ObserveRun(observation report.DatasetObservation) error {
	s.runs = append(s.runs, observation)
	return nil
}

func (s *registryStub) ReplaceExpected(items []report.DatasetExpectation) error {
	s.items = append([]report.DatasetExpectation(nil), items...)
	return nil
}
func (s *registryStub) ObserveInventoryRefreshError() { s.errors++ }

func collectorRule(id string, enabled bool, dataType, target, frequency string) domain.CollectionTask {
	params := `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"` + target + `","frequency":"` + frequency + `"}`
	return domain.CollectionTask{SpaceID: "crypto", TaskID: id, DataType: dataType, CollectParams: params, Enabled: enabled}
}

func tagOnlyCollectorRule(id, target, frequency string) domain.CollectionTask {
	params := `{"target_dataset_id":"` + target + `","frequency":"` + frequency + `"}`
	return domain.CollectionTask{
		SpaceID: "crypto", TaskID: id, DataType: "kline", TagIDs: []string{"binance_spot"},
		CollectParams: params, Enabled: true,
	}
}

func TestRealtimeInventorySelectsEnabledScheduledKlineAndDeduplicates(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.CollectionTask{
		collectorRule("live", true, "kline", "bars", "1m"),
		collectorRule("live-5m", true, "kline", "bars", "5m"),
		collectorRule("duplicate", true, "kline", "bars", "1m"),
		tagOnlyCollectorRule("tag-only", "tag-only-bars", "1m"),
		collectorRule("batch", true, "kline", "batch-bars", "1m"),
		collectorRule("disabled", false, "kline", "disabled-bars", "1m"),
	}}
	registry := &registryStub{}
	inventory := NewRealtimeInventory(source, registry)

	require.NoError(t, inventory.Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1m"}, Interval: time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "5m"}, Interval: 5 * time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "batch-bars", Freq: "1m"}, Interval: time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "tag-only-bars", Freq: "1m"}, Interval: time.Minute},
	}, registry.items)
	require.False(t, inventory.Due(time.Now()))
}

func TestRealtimeInventoryCanonicalizesFrequencyAliasesBeforeDeduplication(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.CollectionTask{
		collectorRule("lowercase", true, "kline", "bars", "1h"),
		collectorRule("canonical", true, "kline", "bars", "1h"),
	}}
	registry := &registryStub{}

	require.NoError(t, NewRealtimeInventory(source, registry).Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{{
		Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1h"}, Interval: time.Hour,
	}}, registry.items)
}

func TestRealtimeInventoryFailureRetainsPreviousSnapshot(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.CollectionTask{collectorRule("live", true, "kline", "bars", "1m")}}
	registry := &registryStub{}
	inventory := NewRealtimeInventory(source, registry)
	require.NoError(t, inventory.Refresh(context.Background()))
	previous := append([]report.DatasetExpectation(nil), registry.items...)

	source.rules = []domain.CollectionTask{{SpaceID: "crypto", TaskID: "invalid", Enabled: true, CollectParams: "{"}}
	inventory.MarkDirty()
	require.Error(t, inventory.Refresh(context.Background()))
	require.Equal(t, previous, registry.items)
	require.Equal(t, 1, registry.errors)
	require.True(t, inventory.Due(time.Now()))

	source.err = errors.New("db unavailable")
	require.Error(t, inventory.Refresh(context.Background()))
	require.Equal(t, previous, registry.items)
}

func TestRealtimeInventoryAcceptsResampleTargetWithoutFrequencySuffix(t *testing.T) {
	// 回归：结果数据集 ID 由任务派生、不带频率后缀，清单刷新曾因后缀检查失败并让 Collector 起不来。
	params := `{"provider":"binance","market_type":"spot","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_resample_task","target_frequency":"5m","alignment":"epoch_utc"}`
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "resample-task", DataType: "kline_resample", CollectParams: params, Enabled: true, PrepareState: domain.PrepareStateReady}
	registry := &registryStub{}
	inventory := NewRealtimeInventory(&ruleSourceStub{rules: []domain.CollectionTask{task}}, registry)

	require.NoError(t, inventory.Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "dataset_resample_task", Freq: "5m"}, Interval: 5 * time.Minute},
	}, registry.items)
}

// 收盘后启用 A 股任务：定时采集路径在 Storage 第一次提交前没有运行记录，登记时记一次“空”运行，Monitor 不会整夜判“尚未上报”；
// 已登记的不重复记，移出后重新进入、进程重启后重新登记都会再记一次。
func TestRealtimeInventoryRecordsAnEmptyRunWhenADatasetIsFirstRegistered(t *testing.T) {
	stock := domain.CollectionTask{SpaceID: "stockcn", TaskID: "stock-5m", DataType: "kline", TagIDs: []string{"cn_hs300"}, CollectParams: `{"target_dataset_id":"dataset_stock_5m","frequency":"5m"}`, Enabled: true}
	source := &ruleSourceStub{rules: []domain.CollectionTask{stock}}
	registry := &registryStub{}
	registeredAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) // 20:00 Asia/Shanghai，已收盘
	inventory := NewRealtimeInventory(source, registry)
	inventory.now = func() time.Time { return registeredAt }
	key := report.DatasetKey{SpaceID: "stockcn", DatasetID: "dataset_stock_5m", Freq: "5m"}

	require.NoError(t, inventory.Refresh(context.Background()))
	require.Equal(t, []report.DatasetObservation{{Key: key, Result: "empty", FinishedAt: registeredAt}}, registry.runs)

	inventory.MarkDirty()
	require.NoError(t, inventory.Refresh(context.Background()))
	require.Len(t, registry.runs, 1, "已登记的数据集不再重复记")

	source.rules = append(source.rules, collectorRule("crypto", true, "kline", "bars", "1m"))
	inventory.MarkDirty()
	require.NoError(t, inventory.Refresh(context.Background()))
	require.Len(t, registry.runs, 2)
	require.Equal(t, report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1m"}, registry.runs[1].Key)

	source.rules = nil
	inventory.MarkDirty()
	require.NoError(t, inventory.Refresh(context.Background()))
	source.rules = []domain.CollectionTask{stock}
	inventory.MarkDirty()
	require.NoError(t, inventory.Refresh(context.Background()))
	require.Len(t, registry.runs, 3, "移出后重新进入清单再记一次")

	// 进程重启：新的清单对象重新登记，同一数据集再记一次（新进程的指标里没有之前的运行时间）。
	restarted := NewRealtimeInventory(source, registry)
	restartedAt := registeredAt.Add(2 * time.Hour)
	restarted.now = func() time.Time { return restartedAt }
	require.NoError(t, restarted.Refresh(context.Background()))
	require.Len(t, registry.runs, 4)
	require.Equal(t, report.DatasetObservation{Key: key, Result: "empty", FinishedAt: restartedAt}, registry.runs[3])
}

// 与真实指标对象一起用：登记后最近运行、最近成功都是登记时刻。
func TestRealtimeInventoryEmptyRunSetsLastRunAndSuccess(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := report.NewDatasetMetrics(registry, "collector")
	require.NoError(t, err)
	stock := domain.CollectionTask{SpaceID: "stockcn", TaskID: "stock-5m", DataType: "kline", TagIDs: []string{"cn_hs300"}, CollectParams: `{"target_dataset_id":"dataset_stock_5m","frequency":"5m"}`, Enabled: true}
	inventory := NewRealtimeInventory(&ruleSourceStub{rules: []domain.CollectionTask{stock}}, metrics)
	registeredAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	inventory.now = func() time.Time { return registeredAt }
	require.NoError(t, inventory.Refresh(context.Background()))

	families, err := registry.Gather()
	require.NoError(t, err)
	found := map[string]float64{}
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			if metric.GetGauge() != nil {
				found[family.GetName()] = metric.GetGauge().GetValue()
			}
		}
	}
	require.Equal(t, float64(registeredAt.Unix()), found["moox_collector_dataset_last_run_timestamp_seconds"])
	require.Equal(t, float64(registeredAt.Unix()), found["moox_collector_dataset_last_success_timestamp_seconds"])
}
