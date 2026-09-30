package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotateTasksAfterAdvancesPastLastCappedTask(t *testing.T) {
	tasks := []domain.CollectionTask{{TaskID: "task-a"}, {TaskID: "task-b"}, {TaskID: "task-c"}}
	rotated := rotateTasksAfter(tasks, "task-a")

	assert.Equal(t, []string{"task-b", "task-c", "task-a"}, taskIDs(rotated))
	assert.Equal(t, []string{"task-a", "task-b", "task-c"}, taskIDs(rotateTasksAfter(tasks, "missing")))
}

func TestNormalizedBatchIdentityUsesNormalizedItemMarketType(t *testing.T) {
	provider, marketType := normalizedBatchIdentity(
		domain.CollectionItem{Provider: "binance", MarketType: "spot"},
	)

	assert.Equal(t, "binance", provider)
	assert.Equal(t, "spot", marketType)
}

func TestCollectionItemInstanceIDIsStableWithinRunAndChangesAcrossRuns(t *testing.T) {
	item := domain.CollectionItem{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DataType: "kline", DatasetID: "bars", Provider: "binance", SourceID: "binance", MarketType: "spot"}
	target := time.Date(2026, 9, 27, 8, 10, 0, 0, time.UTC)
	first := collectionItemInstanceID("crypto", "run-1", "task-a", item, "1m", target)
	assert.Equal(t, first, collectionItemInstanceID("crypto", "run-1", "task-b", item, "1m", target))
	assert.NotEqual(t, first, collectionItemInstanceID("crypto", "run-2", "task-a", item, "1m", target))
	assert.NotEqual(t, sharedCollectionItemKey(item, "1m", target), sharedCollectionItemKey(item, "1m", target.Add(time.Minute)))
}

func TestSharedCollectionItemKeyIncludesRequestSemanticsButExcludesTargets(t *testing.T) {
	target := time.Date(2026, 9, 27, 8, 10, 0, 0, time.UTC)
	base := domain.CollectionItem{
		SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "binance",
		MarketID: "crypto", InstrumentType: "spot", MarketType: "spot", DataType: "kline",
		DatasetID: "bars-a", OutputFields: []string{"close"}, StartTime: "2026-09-27T08:00:00Z", BarLimit: 10,
	}
	baseKey := sharedCollectionItemKey(base, "1m", target)

	targetOnly := base
	targetOnly.DatasetID = "bars-b"
	targetOnly.OutputFields = []string{"close", "volume"}
	assert.Equal(t, baseKey, sharedCollectionItemKey(targetOnly, "1m", target))

	differentLimit := base
	differentLimit.BarLimit = 20
	assert.NotEqual(t, baseKey, sharedCollectionItemKey(differentLimit, "1m", target))

	differentRange := base
	differentRange.StartTime = "2026-09-27T07:00:00Z"
	assert.NotEqual(t, baseKey, sharedCollectionItemKey(differentRange, "1m", target))

	differentMarket := base
	differentMarket.MarketType = "swap"
	assert.NotEqual(t, baseKey, sharedCollectionItemKey(differentMarket, "1m", target))

	retryAttempt := base
	retryAttempt.CandidateIndex = 2
	retryAttempt.SourceEventID = "retry-2"
	assert.Equal(t, baseKey, sharedCollectionItemKey(retryAttempt, "1m", target))
}

func TestFilterNodesByTriggerKeepsInstrumentWorkOnInvokeFleet(t *testing.T) {
	nodes := []scfinvoker.Node{
		{NodeID: "timer-0", TriggerType: "timer"},
		{NodeID: "invoke-0", TriggerType: "invoke"},
	}

	assert.Equal(t, []string{"invoke-0"}, nodeIDs(filterNodesByTrigger(nodes, "invoke")))
	assert.Equal(t, []string{"timer-0"}, nodeIDs(filterNodesByTrigger(nodes, "timer")))
}

func nodeIDs(nodes []scfinvoker.Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.NodeID)
	}
	return ids
}

func TestFilterMarketFetchTasksDropsLocalResampleTasks(t *testing.T) {
	tasks := filterMarketFetchTasks([]domain.CollectionTask{{TaskID: "kline", DataType: "kline"}, {TaskID: "resample", DataType: "kline_resample"}})
	assert.Equal(t, []string{"kline"}, taskIDs(tasks))
}

func TestTargetDataTimeUsesCalendarBoundariesForWeekAndMonth(t *testing.T) {
	now := time.Date(2026, time.July, 29, 15, 47, 12, 0, time.UTC)
	tests := []struct {
		frequency string
		want      time.Time
	}{
		{frequency: "1m", want: time.Date(2026, time.July, 29, 15, 46, 0, 0, time.UTC)},
		{frequency: "1w", want: time.Date(2026, time.July, 20, 0, 0, 0, 0, time.UTC)},
		{frequency: "1M", want: time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.frequency, func(t *testing.T) {
			got, err := targetDataTime(now, tt.frequency)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeStorageFrequencyKeepsWeekAndMonthSemantics(t *testing.T) {
	for input, want := range map[string]string{"1w": "1W", "1M": "1M"} {
		got, err := normalizeStorageFrequency(input)
		assert.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestRealtimeBatchSizeFansOutAcrossCurrentFleet(t *testing.T) {
	nodes := make([]scfinvoker.Node, 10)
	for index := range nodes {
		nodes[index].Metadata = map[string]any{"realtime_batch_size": float64(64)}
	}
	scheduler := &Scheduler{BatchSize: MaxRealtimeItems}
	assert.Equal(t, MaxRealtimeItems, scheduler.realtimeBatchSize(479, nodes))

	nodes[0].Metadata["realtime_batch_size"] = float64(10)
	assert.Equal(t, 10, scheduler.realtimeBatchSize(479, nodes))
}

func TestInvocationCandidatesUsesOneDeterministicFailover(t *testing.T) {
	nodes := []scfinvoker.Node{{NodeID: "node-a"}, {NodeID: "node-b"}, {NodeID: "node-c"}}
	got := invocationCandidates(nodes[1], nodes)
	if assert.Len(t, got, 2) {
		assert.Equal(t, []string{"node-b", "node-c"}, []string{got[0].NodeID, got[1].NodeID})
	}
	assert.Equal(t, []string{"node-a"}, []string{invocationCandidates(nodes[0], nodes[:1])[0].NodeID})
}

func TestBatchKindForTaskUsesRealtimeForKline(t *testing.T) {
	assert.Equal(t, domain.BatchKindRealtime, batchKindForTask(domain.CollectionTask{DataType: "kline"}))
	assert.Equal(t, 70*time.Second, batchCompletionDeadline(domain.BatchKindRealtime))
	assert.Equal(t, 10*time.Second, defaultSCFInvokeAttemptTimeout)
}

func TestExpandTaskUsesExplicitExternalSymbolForKline(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID:         "crypto",
		ResolveSourceID: testSourceID,
		Symbols:         datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
	}
	items, frequencies, err := scheduler.expandTask(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1m"}, frequencies)
	if assert.Len(t, items, 1) {
		assert.Equal(t, "BTCUSDT", items[0].Symbol)
	}
}

func TestExpandTaskCarriesSelectedOutputFieldsToCollectionItems(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID:         "crypto",
		ResolveSourceID: testSourceID,
		Symbols:         datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
	}
	items, _, err := scheduler.expandTask(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m","output_fields":["close","volume"]}`,
	})
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, []string{"close", "volume"}, items[0].OutputFields)
}

func TestExpandTaskKeepsChineseSubjectNames(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID:         "crypto",
		ResolveSourceID: testSourceID,
		Symbols:         datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "币安人生-USDT", Status: "active"}}},
	}
	items, frequencies, err := scheduler.expandTask(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"1m"}, frequencies)
	require.Len(t, items, 1)
	require.Equal(t, "币安人生-USDT", items[0].SubjectID)
	require.Equal(t, "币安人生USDT", items[0].Symbol)
}

func TestPriorityCryptoMinuteItemsSelectsBTCAndETHOnOneMinute(t *testing.T) {
	items := []domain.CollectionItem{
		{SubjectID: "AAA-USDT", DatasetID: "spot_bars"},
		{SubjectID: "BTC-USDT", DatasetID: "spot_bars"},
		{SubjectID: "ETH-USDT", DatasetID: "spot_bars"},
		{SubjectID: "BTC-USDT", DatasetID: "swap_bars"},
	}
	got := priorityCryptoMinuteItems(items, "1m")
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT", "BTC-USDT"}, []string{got[0].SubjectID, got[1].SubjectID, got[2].SubjectID})
	require.Equal(t, []string{"spot_bars", "spot_bars", "swap_bars"}, []string{got[0].DatasetID, got[1].DatasetID, got[2].DatasetID})
	require.Empty(t, priorityCryptoMinuteItems(items, "1h"))
}

func TestPriorityCryptoInvokeNodesKeepSwapOnOverseasEgress(t *testing.T) {
	nodes := []scfinvoker.Node{
		{NodeID: "invoke-nanjing", FunctionName: "fn-nanjing", Region: "ap-nanjing", TriggerType: "invoke"},
		{NodeID: "invoke-hongkong", FunctionName: "fn-hongkong", Region: "ap-hongkong", TriggerType: "invoke"},
	}

	got := priorityNodesForItems([]domain.CollectionItem{{Provider: "binance", MarketType: "swap", DatasetID: "dataset_binance_swap_kline_1m"}}, nodes)
	require.Len(t, got, 1)
	assert.Equal(t, "invoke-hongkong", got[0].NodeID)
	assert.Equal(t, []string{"invoke-hongkong"}, nodeIDsForTest(priorityNodesForItems([]domain.CollectionItem{{Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_spot_kline_1m"}}, nodes)))
}

func nodeIDsForTest(nodes []scfinvoker.Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.NodeID)
	}
	return ids
}

func TestTickSharesOneFetchAcrossTaskWriteTargets(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{
			SpaceID: "crypto", TaskID: "bars-a", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars_a","frequency":"1m","output_fields":["close"]}`,
			ResultViewID: "view-bars-a", Enabled: true,
		},
		{
			SpaceID: "crypto", TaskID: "bars-b", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars_b","frequency":"1m","output_fields":["close","volume"]}`,
			ResultViewID: "view-bars-b", Enabled: true,
		},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	invoker := &recordingMarketFetchInvoker{
		invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}},
	}
	now := time.Date(2026, 9, 27, 8, 10, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(),
		Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return now },
		ResolveSourceID: testSourceID,
		Symbols:         datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)

	invokes := invoker.snapshot()
	data, ok := invokes[0].event["data"].(map[string]any)
	require.True(t, ok)
	targetPayload, ok := data["targets"].([]any)
	require.True(t, ok)
	require.Len(t, targetPayload, 2)
	gotDatasets := make([]string, 0, len(targetPayload))
	for _, raw := range targetPayload {
		target, ok := raw.(map[string]any)
		require.True(t, ok)
		gotDatasets = append(gotDatasets, strings.TrimSpace(fmt.Sprint(target["dataset_id"])))
	}
	require.ElementsMatch(t, []string{"bars_a", "bars_b"}, gotDatasets)

	instances, total, err := db.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto", DataType: "kline", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, instances, 1)
	require.Equal(t, "BTCUSDT", instances[0].ProviderSymbol)
	require.NotNil(t, instances[0].TargetDataTime)
	require.Equal(t, targetDataTimeForTest(now, "1m"), instances[0].TargetDataTime.UTC())
	var requestParams map[string]any
	require.NoError(t, json.Unmarshal([]byte(instances[0].TaskParams), &requestParams))
	require.EqualValues(t, MaxRealtimeRows, requestParams["bar_limit"])
	targets, err := db.TaskInstances().ListWriteTargets(ctx, "crypto", instances[0].InstanceID)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	require.ElementsMatch(t, []string{"bars_a", "bars_b"}, []string{targets[0].DatasetID, targets[1].DatasetID})
}

func TestTimerTickPersistsOneFrozenManifestWithoutInvokingSCF(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", TaskName: "Stock Bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","instrument_type":"equity","subject_tags":["stockcn_equity"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
		ResultViewID:  "view-stock-bars",
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	now := time.Now().UTC().Add(2 * time.Minute).Truncate(time.Minute).Add(8 * time.Second)
	period := targetDataTimeForTest(now, "1m")
	item := domain.CollectionItem{
		SubjectID: "600000.SH", Symbol: "600000", Provider: "stockcn_multi", SourceID: "stockcn_multi", MarketID: StockCNSpaceID,
		InstrumentType: "equity", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m",
		TargetDataTime: period.Format(time.RFC3339Nano), SeriesIndex: 0, ExpectedCount: 1,
	}
	item.SeriesHash = domain.SeriesSetHash([]string{domain.CanonicalSeriesKey(item.Provider, item.SourceID, item.MarketType, item.SubjectID, collectionItemSeriesTag(StockCNSpaceID, item))})
	snapshot, err := periodSeriesSnapshotFromItems(task, StockCNDatasetID, "1m", period, []domain.CollectionItem{item})
	require.NoError(t, err)
	_, _, err = db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	deadline := now.Add(5 * time.Minute)
	routeVersion, sources, err := stockCNAssignmentRoute()
	require.NoError(t, err)
	sourceGroups, err := assignStockCNSourceGroups([]string{item.SubjectID}, sources, 1, MaxRealtimeItems, routeVersion)
	require.NoError(t, err)
	assignment := NodeAssignment{
		NodeID: "timer-1", FunctionName: "market-fetch-1", Region: "ap-shanghai", Provider: sourceGroups[0].Source.Provider, RouteProvider: "stockcn_multi",
		SourceID: sourceGroups[0].Source.SourceID, MarketType: "equity", MarketID: StockCNSpaceID, InstrumentType: "equity", DatasetID: StockCNDatasetID,
		Frequency: "1m", RouteVersion: routeVersion, GroupID: 0, GroupCount: 1, Subjects: []string{item.SubjectID}, Enabled: true,
	}
	invoker := &recordingMarketFetchInvoker{
		invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "invoke-fetch", Region: "ap-hongkong", TriggerType: "invoke"}},
		timerNodes:  []scfinvoker.Node{{NodeID: assignment.NodeID, FunctionName: assignment.FunctionName, Region: assignment.Region, TriggerType: "timer"}},
	}
	planner := &TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(),
		ValidTargetDataTime: func(string, time.Time) bool { return true },
		Now:                 func() time.Time { return now },
		EnsureStorage: func(_ context.Context, frozen domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			return timerPlannerStorageState(frozen, deadline), nil
		},
	}
	scheduler := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
		PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), PeriodStorageStates: db.PeriodStorageStates(), TimerPeriodBatches: db.TimerPeriodBatches(),
		TimerPeriodPlanner: planner, TimerAssignments: func() []NodeAssignment { return []NodeAssignment{assignment} },
		Invoker: invoker, SpaceID: StockCNSpaceID, InvokeNonRealtimeOnly: true, Now: func() time.Time { return now },
		ResolveSourceID: testSourceID, TimerMeasuredSafeGroupSize: MaxRealtimeItems,
	}
	require.NoError(t, scheduler.Tick(ctx, StockCNSpaceID))
	first, err := db.TimerPeriodBatches().GetByPeriod(ctx, snapshot.Key, 0)
	require.NoError(t, err)
	require.NotEmpty(t, first.FirstRunID)
	firstBatch, err := db.FetchBatches().Get(ctx, StockCNSpaceID, first.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, firstBatch.Status)
	items, err := db.FetchBatches().ListItems(ctx, StockCNSpaceID, first.BatchID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Empty(t, invoker.snapshot(), "Timer initial manifests are claimed by the runtime, not directly invoked")

	require.NoError(t, scheduler.Tick(ctx, StockCNSpaceID))
	restarted := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
		PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), PeriodStorageStates: db.PeriodStorageStates(), TimerPeriodBatches: db.TimerPeriodBatches(),
		TimerPeriodPlanner: planner, TimerAssignments: func() []NodeAssignment { return []NodeAssignment{assignment} },
		Invoker: invoker, SpaceID: StockCNSpaceID, InvokeNonRealtimeOnly: true, Now: func() time.Time { return now },
		ResolveSourceID: testSourceID, TimerMeasuredSafeGroupSize: MaxRealtimeItems,
	}
	require.NoError(t, restarted.Tick(ctx, StockCNSpaceID))
	second, err := db.TimerPeriodBatches().GetByPeriod(ctx, snapshot.Key, 0)
	require.NoError(t, err)
	require.Equal(t, first.BatchID, second.BatchID, "restart and duplicate tick keep the original stable batch")
	require.Equal(t, first.FirstRunID, second.FirstRunID, "restart cannot replace the period owner Run")
	items, err = db.FetchBatches().ListItems(ctx, StockCNSpaceID, first.BatchID)
	require.NoError(t, err)
	require.Len(t, items, 1, "duplicate tick cannot append batch items")
	require.Empty(t, invoker.snapshot())
}

func TestTimerPlanningFreezesAllGroupMembershipWhenAGroupIsUnavailable(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: StockCNSpaceID, TaskID: "stock-bars", TaskName: "Stock Bars", DataType: "kline", Enabled: true,
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","instrument_type":"equity","target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
		ResultViewID:  "view-stock-bars",
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	period := time.Date(2026, 9, 30, 2, 14, 0, 0, time.UTC)
	items := []domain.CollectionItem{
		{SubjectID: "000001.SZ", Symbol: "000001", Provider: "stockcn_multi", SourceID: "stockcn_multi", MarketID: StockCNSpaceID, InstrumentType: "equity", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", TargetDataTime: period.Format(time.RFC3339Nano)},
		{SubjectID: "600000.SH", Symbol: "600000", Provider: "stockcn_multi", SourceID: "stockcn_multi", MarketID: StockCNSpaceID, InstrumentType: "equity", MarketType: "equity", DataType: "kline", DatasetID: StockCNDatasetID, Frequency: "1m", TargetDataTime: period.Format(time.RFC3339Nano)},
	}
	keys := []string{
		domain.CanonicalSeriesKey(items[0].Provider, items[0].SourceID, items[0].MarketType, items[0].SubjectID, "default"),
		domain.CanonicalSeriesKey(items[1].Provider, items[1].SourceID, items[1].MarketType, items[1].SubjectID, "default"),
	}
	seriesHash := domain.SeriesSetHash(keys)
	for index := range items {
		items[index].SeriesIndex = uint32(index)
		items[index].SeriesHash = seriesHash
		items[index].ExpectedCount = uint32(len(items))
	}
	snapshot, err := periodSeriesSnapshotFromItems(task, StockCNDatasetID, "1m", period, items)
	require.NoError(t, err)
	_, _, err = db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	deadline := period.Add(5 * time.Minute)
	planner := &TimerPeriodPlanner{
		Snapshots: db.PeriodSeriesSnapshot(), States: db.PeriodStorageStates(), Batches: db.TimerPeriodBatches(),
		Now: func() time.Time { return period.Add(time.Minute) },
		EnsureStorage: func(_ context.Context, frozen domain.PeriodSeriesSnapshot) (domain.PeriodStorageState, error) {
			return timerPlannerStorageState(frozen, deadline), nil
		},
	}
	routeVersion, sources, err := stockCNAssignmentRoute()
	require.NoError(t, err)
	subjects := []string{items[0].SubjectID, items[1].SubjectID}
	sourceGroups, err := assignStockCNSourceGroups(subjects, sources, 2, MaxRealtimeItems, routeVersion)
	require.NoError(t, err)
	assignments := make([]NodeAssignment, len(sourceGroups))
	for groupID, sourceGroup := range sourceGroups {
		nodeID := fmt.Sprintf("timer-%d", groupID+1)
		assignments[groupID] = NodeAssignment{
			NodeID: nodeID, FunctionName: fmt.Sprintf("market-fetch-%d", groupID), Region: "ap-shanghai",
			Provider: sourceGroup.Source.Provider, RouteProvider: "stockcn_multi", SourceID: sourceGroup.Source.SourceID,
			MarketType: "equity", MarketID: StockCNSpaceID, InstrumentType: "equity", DatasetID: StockCNDatasetID,
			Frequency: "1m", RouteVersion: routeVersion, GroupID: groupID, GroupCount: len(sourceGroups),
			Subjects: append([]string(nil), sourceGroup.Subjects...), Enabled: true,
		}
	}
	scheduler := &Scheduler{
		TimerPeriodPlanner: planner, PeriodStorageStates: db.PeriodStorageStates(), SpaceID: StockCNSpaceID,
		TimerMeasuredSafeGroupSize: MaxRealtimeItems,
	}
	available := []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "market-fetch-0", Region: "ap-shanghai", TriggerType: "timer"}}
	created, err := scheduler.planTimerPeriodForAssignments(ctx, StockCNSpaceID, "run-first", time.Time{}, task, "1m", period, period.Add(time.Minute), assignments, available)
	require.NoError(t, err)
	require.Zero(t, created, "a missing group node must prevent partial period persistence")
	manifests, err := db.TimerPeriodBatches().ListByPeriod(ctx, snapshot.Key)
	require.NoError(t, err)
	require.Empty(t, manifests, "a partially available node catalog must leave no manifests")
	for groupID := range sourceGroups {
		batchID := stableID(StockCNSpaceID, StockCNDatasetID, "1m", period.Format(time.RFC3339Nano), fmt.Sprint(groupID), "timer-initial")
		_, batchErr := db.FetchBatches().Get(ctx, StockCNSpaceID, batchID)
		require.Error(t, batchErr, "a missing group node must leave no batch rows")
	}
	instances, _, err := db.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: StockCNSpaceID, CollectionTaskID: task.TaskID, PageSize: 20})
	require.NoError(t, err)
	require.Empty(t, instances, "a missing group node must leave no instances or targets")

	assignments[0].Subjects = []string{"600000.SH", "000001.SZ"}
	assignments[1].Subjects = nil
	available = []scfinvoker.Node{
		{NodeID: "timer-1", FunctionName: "market-fetch-0", Region: "ap-shanghai", TriggerType: "timer"},
		{NodeID: "timer-2", FunctionName: "market-fetch-1", Region: "ap-shanghai", TriggerType: "timer"},
	}
	created, err = scheduler.planTimerPeriodForAssignments(ctx, StockCNSpaceID, "run-after-label-change", time.Time{}, task, "1m", period, period.Add(time.Minute), assignments, available)
	require.NoError(t, err)
	require.Equal(t, 2, created, "the retry must atomically persist all nonempty groups from the snapshot")
	manifests, err = db.TimerPeriodBatches().ListByPeriod(ctx, snapshot.Key)
	require.NoError(t, err)
	require.Len(t, manifests, 2)

	before := make(map[uint32]string, len(manifests))
	for shard := range sourceGroups {
		manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, snapshot.Key, uint32(shard))
		require.NoError(t, err)
		batch, err := db.FetchBatches().Get(ctx, StockCNSpaceID, manifest.BatchID)
		require.NoError(t, err)
		var request Request
		require.NoError(t, json.Unmarshal([]byte(batch.RequestJSON), &request))
		want := append([]string(nil), sourceGroups[shard].Subjects...)
		got := make([]string, len(request.Items))
		for index, item := range request.Items {
			got[index] = item.SubjectID
		}
		sort.Strings(want)
		sort.Strings(got)
		require.Equal(t, want, got, "group membership must come from the frozen snapshot, not refreshed assignment subjects")
		before[uint32(shard)] = batch.RequestJSON
	}

	assignments[0].Subjects = []string{"000001.SZ"}
	assignments[1].Subjects = []string{"600000.SH"}
	created, err = scheduler.planTimerPeriodForAssignments(ctx, StockCNSpaceID, "run-after-second-label-change", time.Time{}, task, "1m", period, period.Add(time.Minute), assignments, available)
	require.NoError(t, err)
	require.Zero(t, created, "an existing manifest set is immutable across later assignment refreshes")
	manifests, err = db.TimerPeriodBatches().ListByPeriod(ctx, snapshot.Key)
	require.NoError(t, err)
	require.Len(t, manifests, 2)
	for shard, requestJSON := range before {
		manifest, err := db.TimerPeriodBatches().GetByPeriod(ctx, snapshot.Key, shard)
		require.NoError(t, err)
		batch, err := db.FetchBatches().Get(ctx, StockCNSpaceID, manifest.BatchID)
		require.NoError(t, err)
		require.Equal(t, requestJSON, batch.RequestJSON)
	}
}

func TestSchedulerExpansionCachesSharedTagAndSkipsDatasetLookup(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", TagIDs: []string{"shared"}, CollectParams: `{"target_dataset_id":"bars-a","frequency":"1m"}`, Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", TagIDs: []string{"shared"}, CollectParams: `{"target_dataset_id":"bars-b","frequency":"1m"}`, Enabled: true},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	source := &countingTagDatasetSource{
		tag:      &storagepb.Tag{SpaceId: "crypto", TagId: "shared", Source: "binance", MarketType: "spot"},
		subjects: []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}},
	}
	now := time.Date(2026, 9, 29, 3, 10, 5, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(),
		Invoker: &recordingMarketFetchInvoker{invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}},
		SpaceID: "crypto", Now: func() time.Time { return now }, ResolveSourceID: testSourceID, Symbols: source,
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Zero(t, source.getDatasetCalls, "task-owned tags must avoid the legacy Dataset lookup")
	require.Equal(t, 1, source.getTagCalls, "tasks sharing one tag should resolve its route once per scheduler tick")
	require.Equal(t, 1, source.resolveSubjectsCalls, "tasks sharing one tag should resolve its members once per scheduler tick")
}

func TestSharedFetchKeepsDatasetSpecificSeriesIndexAndHash(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", TagIDs: []string{"tag-a"}, CollectParams: `{"target_dataset_id":"bars-a","frequency":"1m"}`, Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", TagIDs: []string{"tag-b"}, CollectParams: `{"target_dataset_id":"bars-b","frequency":"1m"}`, Enabled: true},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	source := tagAwareDatasetSourceStub{
		tags: map[string]*storagepb.Tag{
			"tag-a": {SpaceId: "crypto", TagId: "tag-a", Source: "binance", MarketType: "spot"},
			"tag-b": {SpaceId: "crypto", TagId: "tag-b", Source: "binance", MarketType: "spot"},
		},
		subjects: map[string][]domain.Subject{
			"tag-a": {{SubjectID: "BTC-USDT", Status: "active"}},
			"tag-b": {{SubjectID: "ADA-USDT", Status: "active"}, {SubjectID: "BTC-USDT", Status: "active"}},
		},
	}
	now := time.Date(2026, 9, 28, 6, 30, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(),
		Invoker: &recordingMarketFetchInvoker{invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}},
		SpaceID: "crypto", Now: func() time.Time { return now }, ResolveSourceID: testSourceID, Symbols: source,
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	instances, _, err := db.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto", SubjectID: "BTC-USDT", Frequency: "1m", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, instances, 1, "the provider BTC request is shared across tasks")
	targets, err := db.TaskInstances().ListWriteTargets(ctx, "crypto", instances[0].InstanceID)
	require.NoError(t, err)
	require.Len(t, targets, 2)
	byDataset := make(map[string]domain.WriteTarget, len(targets))
	for _, target := range targets {
		byDataset[target.DatasetID] = target
	}
	require.Equal(t, uint32(0), byDataset["bars-a"].SeriesIndex)
	require.Equal(t, uint32(1), byDataset["bars-a"].ExpectedCount)
	require.Equal(t, uint32(1), byDataset["bars-b"].SeriesIndex, "ADA sorts before BTC in task-b's Dataset series set")
	require.Equal(t, uint32(2), byDataset["bars-b"].ExpectedCount)
	require.NotEmpty(t, byDataset["bars-a"].SeriesHash)
	require.NotEmpty(t, byDataset["bars-b"].SeriesHash)
	require.NotEqual(t, byDataset["bars-a"].SeriesHash, byDataset["bars-b"].SeriesHash)
}

func TestDispatchDueRetriesRespectsRetryScopeTargets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		retryScope    string
		writeTargetID string
		wantDatasets  []string
	}{
		{name: "fetch fans out to all enabled targets", retryScope: "fetch", wantDatasets: []string{"bars-a", "bars-b"}},
		{name: "write target retries one destination", retryScope: "write_target", writeTargetID: "target-b", wantDatasets: []string{"bars-b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestMarketFetchStore(t)
			ctx := context.Background()
			for _, task := range []domain.CollectionTask{
				{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", Enabled: true},
				{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", Enabled: true},
			} {
				require.NoError(t, db.Tasks().Create(ctx, task))
			}
			instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "shared", Provider: "binance", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m"}
			require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
			require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
				{ID: "target-a", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "task-a", DatasetID: "bars-a", OutputFields: `["close"]`, Status: "pending"},
				{ID: "target-b", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: "task-b", DatasetID: "bars-b", OutputFields: `["close","volume"]`, Status: "pending"},
			}))
			now := time.Date(2026, 9, 27, 8, 20, 0, 0, time.UTC)
			item := domain.CollectionItem{InstanceID: instance.InstanceID, SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars-a", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}
			raw, err := json.Marshal(item)
			require.NoError(t, err)
			require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
				SpaceID: "crypto", RetryKey: "retry-1", SourceBatchID: "sync-1", BatchKind: domain.BatchKindRealtime,
				InstanceID: instance.InstanceID, WriteTargetID: tc.writeTargetID, RetryScope: tc.retryScope,
				SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: now.Add(-time.Minute), TaskJSON: string(raw),
				Attempt: 1, Status: "pending", NextRetryAt: &now,
			}))
			invoker := &recordingMarketFetchInvoker{}
			nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
			scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, Now: func() time.Time { return now }}
			require.NoError(t, scheduler.dispatchDueRetries(ctx, "crypto", nodes, now))
			require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
			data, ok := invoker.snapshot()[0].event["data"].(map[string]any)
			require.True(t, ok)
			targetPayload, ok := data["targets"].([]any)
			require.True(t, ok)
			gotDatasets := make([]string, 0, len(targetPayload))
			for _, rawTarget := range targetPayload {
				target, ok := rawTarget.(map[string]any)
				require.True(t, ok)
				gotDatasets = append(gotDatasets, strings.TrimSpace(fmt.Sprint(target["dataset_id"])))
			}
			require.ElementsMatch(t, tc.wantDatasets, gotDatasets)
		})
	}
}

func TestDispatchDuePeriodRetryPreservesPeriodCommitRequirement(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, taskID, instanceID = "crypto", "period-retry-task", "period-retry-instance"
	period := time.Date(2026, 9, 27, 8, 20, 0, 0, time.UTC)
	item := domain.CollectionItem{
		InstanceID: instanceID, SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "spot_http",
		MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m",
		TargetDataTime: period.Format(time.RFC3339Nano), SeriesIndex: 0, SeriesHash: "period-hash", ExpectedCount: 1,
		RequirePeriodCommit: true,
	}
	target := domain.WriteTarget{
		ID: "period-retry-target", SpaceID: spaceID, InstanceID: instanceID, TaskID: taskID, DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: item.SeriesHash, ExpectedCount: item.ExpectedCount, Status: "pending",
	}
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: spaceID, TaskID: taskID, DataType: "kline", Enabled: true}))
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{
		SpaceID: spaceID, InstanceID: instanceID, SubjectID: item.SubjectID, Frequency: item.Frequency, TaskParams: `{}`,
	}}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	now := period.Add(time.Minute)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: spaceID, RetryKey: "period-retry", SourceBatchID: "period-retry-source", BatchKind: domain.BatchKindRealtime,
		InstanceID: instanceID, RetryScope: "fetch", SubjectID: item.SubjectID, Frequency: item.Frequency,
		TargetDataTime: period, TaskJSON: string(itemJSON), Attempt: 1, Status: "pending", NextRetryAt: &now,
	}))
	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, Now: func() time.Time { return now }}
	nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	data, ok := invoker.snapshot()[0].event["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, data["require_period_commit"])
	requestItems, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, requestItems, 1)
	requestItem, ok := requestItems[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, requestItem["require_period_commit"])
}

func TestRecoverDuePreservesWriteTargetRetryScopeAndAttempt(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const retryKey = "instance-eth:target:target-eth"
	period := time.Date(2026, 9, 30, 11, 59, 0, 0, time.UTC)
	item := domain.CollectionItem{
		InstanceID: "instance-eth", SubjectID: "ETH-USDT", Symbol: "ETHUSDT", DatasetID: "bars", Frequency: "1m",
		Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline",
		TargetDataTime: period.Format(time.RFC3339Nano), SourceEventID: retryKey,
	}
	itemRaw, err := json.Marshal(item)
	require.NoError(t, err)
	target := domain.WriteTarget{
		ID: "target-eth", SpaceID: "crypto", InstanceID: item.InstanceID, TaskID: "bars-task", DatasetID: "bars",
		SeriesIndex: 0, SeriesHash: "series-hash", ExpectedCount: 1, Status: "pending",
	}
	targetRaw, err := json.Marshal([]domain.WriteTarget{target})
	require.NoError(t, err)
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: "crypto", TaskID: "bars-task", DataType: "kline", Enabled: true}))
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: item.InstanceID, SubjectID: item.SubjectID, Frequency: item.Frequency, TaskParams: `{}`}}))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: retryKey, InstanceID: item.InstanceID, WriteTargetID: target.ID, RetryScope: "write_target",
		SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: period, TaskJSON: string(itemRaw),
		FailureTargetsJSON: string(targetRaw), Attempt: 1, Status: "dispatched",
	}))
	request := Request{BatchID: "target-timeout", ScheduleID: "retry:" + retryKey, BatchKind: domain.BatchKindRealtime,
		SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target}}
	requestRaw, err := json.Marshal(request)
	require.NoError(t, err)
	now := time.Now().UTC()
	deadline := now.Add(-time.Second)
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		Status: domain.BatchStatusPlanned, Attempt: 2, RetryScope: "write_target", WriteTargetID: target.ID,
		Frequency: "1m", RequestJSON: string(requestRaw), PlannedCount: 1, PlannedAt: &now, DeadlineAt: &deadline,
	}
	created, err := db.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{item.InstanceID}))
	_, err = db.FetchBatches().MarkDispatchedToNode(ctx, "crypto", batch.BatchID, "request-1", deadline, "region", "node", "fn")
	require.NoError(t, err)

	scheduler := &Scheduler{Batches: db.FetchBatches(), Retries: db.FetchRetries()}
	require.NoError(t, scheduler.recoverDue(ctx, "crypto", nil, now))
	recovered, err := db.FetchRetries().Get(ctx, "crypto", retryKey)
	require.NoError(t, err)
	require.Equal(t, "pending", recovered.Status)
	require.Equal(t, 2, recovered.Attempt)
	require.Equal(t, "write_target", recovered.RetryScope)
	require.Equal(t, target.ID, recovered.WriteTargetID)
	var failures []domain.WriteTarget
	require.NoError(t, json.Unmarshal([]byte(recovered.FailureTargetsJSON), &failures))
	require.Equal(t, []domain.WriteTarget{target}, failures)
}

func TestRecoverDueDoesNotResurrectSupersededRetry(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, instanceID, retryKey = "crypto", "superseded-instance", "superseded-retry"
	now := time.Now().UTC()
	period := now.Add(-time.Minute)
	item := domain.CollectionItem{
		InstanceID: instanceID, SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DatasetID: "bars", Frequency: "1m",
		Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline",
		TargetDataTime: period.Format(time.RFC3339Nano), SourceEventID: retryKey,
	}
	itemRaw, err := json.Marshal(item)
	require.NoError(t, err)
	request := Request{
		BatchID: "superseded-planned-timeout", SyncPointID: "sync-superseded", ScheduleID: "retry:superseded-planned-timeout",
		BatchKind: domain.BatchKindRealtime, SpaceID: spaceID, DatasetID: "bars", Frequency: "1m", Items: []domain.CollectionItem{item},
	}
	requestRaw, err := json.Marshal(request)
	require.NoError(t, err)
	deadline := now.Add(-time.Second)
	batch := &domain.BatchInvocation{
		SpaceID: spaceID, BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		Frequency: request.Frequency, Status: domain.BatchStatusPlanned, Attempt: 2, RequestJSON: string(requestRaw),
		PlannedCount: 1, PlannedAt: &now, DeadlineAt: &deadline,
	}
	created, err := db.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.FetchBatches().UpsertItems(ctx, spaceID, batch.BatchID, []string{instanceID}))
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: spaceID, RetryKey: retryKey, SourceBatchID: request.SyncPointID, BatchKind: request.BatchKind,
		InstanceID: instanceID, SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: period,
		TaskJSON: string(itemRaw), Attempt: 1, Status: "superseded",
	}))

	scheduler := &Scheduler{Batches: db.FetchBatches(), Retries: db.FetchRetries()}
	require.NoError(t, scheduler.recoverDue(ctx, spaceID, nil, now))
	storedBatch, err := db.FetchBatches().Get(ctx, spaceID, batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusTimedOut, storedBatch.Status)
	storedRetry, err := db.FetchRetries().Get(ctx, spaceID, retryKey)
	require.NoError(t, err)
	require.Equal(t, "superseded", storedRetry.Status, "planned timeout recovery must not revive a retry retired by a newer period")
}

func TestSchedulerRecoversDueWorkWithoutInvokeNodesBeforeRefusingPlanning(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "http-task", TaskName: "HTTP task", DataType: "http", Enabled: true}
	require.NoError(t, db.Tasks().Create(ctx, task))
	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "timeout-instance", RunID: "timeout-run", DataType: "http", SubjectID: "BTC-USDT", Frequency: "1m"}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	target := domain.WriteTarget{ID: "timeout-target", SpaceID: "crypto", InstanceID: instance.InstanceID, TaskID: task.TaskID, DatasetID: "bars", Status: "pending"}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	item := domain.CollectionItem{
		InstanceID: instance.InstanceID, SubjectID: instance.SubjectID, Symbol: "BTCUSDT", Provider: "binance", SourceID: "spot_http",
		MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano),
	}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	request := Request{BatchID: "timeout-batch", ScheduleID: "timeout-batch", BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", Items: []domain.CollectionItem{item}}
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)
	plannedAt := now.Add(-2 * time.Minute)
	deadline := now.Add(-time.Second)
	batch := &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		Status: domain.BatchStatusPlanned, Attempt: 1, Frequency: "1m", RequestJSON: string(requestJSON), PlannedCount: 1,
		PlannedAt: &plannedAt, DeadlineAt: &deadline,
	}
	created, err := db.FetchBatches().CreatePlanned(ctx, batch)
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, db.FetchBatches().UpsertItems(ctx, "crypto", batch.BatchID, []string{instance.InstanceID}))
	_, err = db.FetchBatches().MarkDispatchedToNode(ctx, "crypto", batch.BatchID, "timeout-request", deadline, "region", "node", "fetch")
	require.NoError(t, err)
	dueRetryAt := now
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "already-due", Status: "pending", NextRetryAt: &dueRetryAt,
		TaskJSON: string(itemJSON), CreateTime: now.Add(-time.Minute),
	}))

	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(),
		Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return now },
	}
	err = scheduler.Tick(ctx, "crypto")
	require.ErrorContains(t, err, "no active Invoke market fetcher nodes")
	recovered, err := db.FetchBatches().Get(ctx, "crypto", batch.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusTimedOut, recovered.Status)
	timeoutRetryKey := retryKey(batch.BatchID, item.SubjectID, item.TargetDataTime)
	timeoutRetry, err := db.FetchRetries().Get(ctx, "crypto", timeoutRetryKey)
	require.NoError(t, err)
	require.Equal(t, "pending", timeoutRetry.Status)
	queuedRetry, err := db.FetchRetries().Get(ctx, "crypto", "already-due")
	require.NoError(t, err)
	require.Equal(t, "pending", queuedRetry.Status, "due retries remain queued without dispatch capacity")
	require.Empty(t, invoker.snapshot(), "no new Invoke-dependent plan is dispatched without Invoke nodes")
}

func TestDispatchDueRetriesKeepsQueueWhenNoCapacityExists(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	retry := &domain.RetryItem{SpaceID: "crypto", RetryKey: "queued", Status: "pending", NextRetryAt: &now, CreateTime: now}
	require.NoError(t, db.FetchRetries().Upsert(ctx, retry))
	scheduler := &Scheduler{Retries: db.FetchRetries(), Batches: db.FetchBatches()}

	require.NoError(t, scheduler.dispatchDueRetries(ctx, "crypto", nil, now))
	stored, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, "pending", stored.Status, "lack of capacity must not consume or permanently fail retry work")
	dueBatches, err := db.FetchBatches().ListDue(ctx, "crypto", now, 10)
	require.NoError(t, err)
	require.Empty(t, dueBatches)
}

func TestDispatchDueRetriesMarksExhaustedAttemptPermanentAndWakesReporterWithoutCapacity(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	retry := &domain.RetryItem{SpaceID: "crypto", RetryKey: "exhausted", Status: "pending", Attempt: 4, NextRetryAt: &now, CreateTime: now}
	require.NoError(t, db.FetchRetries().Upsert(ctx, retry))
	wakeCalls := 0
	scheduler := &Scheduler{Retries: db.FetchRetries(), Batches: db.FetchBatches(), MaxRetryAttempts: 3, WakePeriodFailureReporter: func() { wakeCalls++ }}

	require.NoError(t, scheduler.dispatchDueRetries(ctx, "crypto", nil, now))
	stored, err := db.FetchRetries().Get(ctx, "crypto", retry.RetryKey)
	require.NoError(t, err)
	require.Equal(t, "permanent_failed", stored.Status)
	require.Equal(t, "retry_budget_exhausted", stored.LastErrorType)
	require.Equal(t, 1, wakeCalls, "the durable permanent failure must wake its reporter immediately")
}

func TestSchedulerRecoversAndDispatchesConfiguredTimerWavesWithinCapacity(t *testing.T) {
	route, err := loadStockCNRouteFile(filepath.Join("..", "..", "config", "markets", "stockcn", "route.yaml"))
	require.NoError(t, err)
	groups, itemsPerGroup := route.TimerFunctionCount, route.MeasuredSafeGroupSize
	require.Equal(t, 170, groups)
	require.Equal(t, MaxRealtimeItems, itemsPerGroup)
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: StockCNSpaceID, TaskID: "stock-wave-task", TaskName: "Stock Wave", DataType: "kline", Enabled: true}))

	instances := make([]domain.TaskInstance, itemsPerGroup)
	instanceIDs := make([]string, itemsPerGroup)
	targets := make([]domain.WriteTarget, itemsPerGroup)
	for index := 0; index < itemsPerGroup; index++ {
		instanceID := fmt.Sprintf("stock-wave-instance-%02d", index)
		instanceIDs[index] = instanceID
		instances[index] = domain.TaskInstance{SpaceID: StockCNSpaceID, InstanceID: instanceID, Provider: "sina", ProviderSymbol: fmt.Sprintf("sh%06d", index), SourceID: "stockcn_minute_http", MarketType: "equity", DataType: "kline", SubjectID: fmt.Sprintf("%06d.XSHG", index), Frequency: "1m", TaskParams: `{}`}
		targets[index] = domain.WriteTarget{ID: "stock-wave-target-" + instanceID, SpaceID: StockCNSpaceID, InstanceID: instanceID, TaskID: "stock-wave-task", DatasetID: StockCNDatasetID, Status: "pending"}
	}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, instances))
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, targets))

	nodes := make([]scfinvoker.Node, groups)
	for index := range nodes {
		nodes[index] = scfinvoker.Node{NodeID: fmt.Sprintf("invoke-%03d", index), FunctionName: fmt.Sprintf("fetch-%03d", index), Region: "ap-singapore", TriggerType: "invoke"}
	}
	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{
		Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker,
		InvokeConcurrency: 96,
	}
	firstTick := time.Now().UTC().Truncate(time.Minute).Add(10 * time.Second)
	for wave := 0; wave < 2; wave++ {
		tick := firstTick.Add(time.Duration(wave) * time.Minute)
		for group := 0; group < groups; group++ {
			batchID := fmt.Sprintf("stock-wave-%d-batch-%03d", wave, group)
			items := make([]domain.CollectionItem, itemsPerGroup)
			for index, instance := range instances {
				items[index] = domain.CollectionItem{
					InstanceID: instance.InstanceID, SubjectID: instance.SubjectID, Symbol: instance.ProviderSymbol,
					TargetDataTime: tick.Add(-time.Minute).Format(time.RFC3339Nano), Provider: instance.Provider, SourceID: instance.SourceID,
					MarketID: StockCNSpaceID, InstrumentType: "equity", MarketType: instance.MarketType, DataType: instance.DataType,
					DatasetID: StockCNDatasetID, Frequency: "1m", BarLimit: MaxRealtimeRows,
				}
			}
			request := Request{
				BatchID: batchID, SyncPointID: fmt.Sprintf("stock-wave-%d-sync-%03d", wave, group), ScheduleID: batchID,
				BatchKind: domain.BatchKindRealtime, SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID, Frequency: "1m",
				Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", Items: items,
			}
			raw, marshalErr := json.Marshal(request)
			require.NoError(t, marshalErr)
			deadline := tick.Add(-time.Second)
			batch := &domain.BatchInvocation{
				SpaceID: StockCNSpaceID, BatchID: batchID, ScheduleID: batchID, BatchKind: domain.BatchKindRealtime,
				Frequency: "1m", Status: domain.BatchStatusPlanned, Attempt: 1, RequestJSON: string(raw), PlannedCount: itemsPerGroup,
				PlannedAt: timePtr(tick.Add(-time.Minute)), DeadlineAt: &deadline,
			}
			created, createErr := db.FetchBatches().CreatePlanned(ctx, batch)
			require.NoError(t, createErr)
			require.True(t, created)
			require.NoError(t, db.FetchBatches().UpsertItems(ctx, StockCNSpaceID, batchID, instanceIDs))
			_, dispatchErr := db.FetchBatches().MarkDispatchedToNode(ctx, StockCNSpaceID, batchID, "timer-request", deadline, "ap-singapore", "timer-node", "timer-function")
			require.NoError(t, dispatchErr)
		}

		before, err := db.FetchRetries().CountPending(ctx, StockCNSpaceID, StockCNDatasetID, "1m")
		require.NoError(t, err)
		require.Zero(t, before, "previous wave must be fully drained before new arrivals")
		require.NoError(t, scheduler.recoverDue(ctx, StockCNSpaceID, nodes, tick))
		afterRecovery, err := db.FetchRetries().CountPending(ctx, StockCNSpaceID, StockCNDatasetID, "1m")
		require.NoError(t, err)
		require.EqualValues(t, groups*itemsPerGroup, afterRecovery, "bounded recovery pass must recover every item from the configured wave")
		require.NoError(t, scheduler.dispatchDueRetries(ctx, StockCNSpaceID, nodes, tick.Add(5*time.Second)))
		wantInvocations := (wave + 1) * groups
		require.Eventually(t, func() bool {
			pending, countErr := db.FetchRetries().CountPending(ctx, StockCNSpaceID, StockCNDatasetID, "1m")
			return countErr == nil && pending == 0 && len(invoker.snapshot()) == wantInvocations
		}, 10*time.Second, 10*time.Millisecond, "all compatible fetch retries should dispatch in one bounded pass")
		require.Equal(t, 96, cap(scheduler.invokeSem), "StockCN needs enough slots to drain 170 concurrent batches in two worst-case waves")
		waveCount := (groups + cap(scheduler.invokeSem) - 1) / cap(scheduler.invokeSem)
		require.Equal(t, 2, waveCount)
		waveDuration := time.Duration(waveCount) * 2 * defaultSCFInvokeAttemptTimeout
		require.Equal(t, 40*time.Second, waveDuration, "ceil(170/96) waves at two 10-second attempts each")
		require.Less(t, waveDuration, time.Minute, "configured retry wave must leave scheduler-period headroom")
		require.LessOrEqual(t, cap(scheduler.invokeSem), 96, "retry dispatch concurrency must remain bounded")
		totalItems := 0
		for _, invoke := range invoker.snapshot() {
			data, ok := invoke.event["data"].(map[string]any)
			require.True(t, ok)
			requestItems, ok := data["items"].([]any)
			require.True(t, ok)
			require.Len(t, requestItems, itemsPerGroup, "one original source batch should remain one bounded retry request")
			totalItems += len(requestItems)
		}
		require.Equal(t, wantInvocations*itemsPerGroup, totalItems)
	}
}

func TestRetryDispatchWaitBudgetCoversBoundedQueueAtConfiguredConcurrency(t *testing.T) {
	require.Equal(t, 90*time.Second, retryDispatchWaitTimeout(StockCNTimerInvokeConcurrency), "256 batches at 96 slots require three 20-second waves plus 30 seconds of dispatch/database slack")
	require.Equal(t, 290*time.Second, retryDispatchWaitTimeout(DefaultInvokeConcurrency), "the default 20-slot scheduler also needs a finite budget covering all 256 batches")
}

type deadlineGatedMarketFetchInvoker struct {
	deadlines   chan time.Time
	release     chan struct{}
	releaseOnce sync.Once
}

func (g *deadlineGatedMarketFetchInvoker) unblock() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func (g *deadlineGatedMarketFetchInvoker) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (g *deadlineGatedMarketFetchInvoker) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (g *deadlineGatedMarketFetchInvoker) Invoke(ctx context.Context, _, _ string, _ map[string]any, _ cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	deadline, _ := ctx.Deadline()
	g.deadlines <- deadline
	select {
	case <-g.release:
		return scfinvoker.InvocationResult{RequestID: "gated-request"}, nil
	case <-ctx.Done():
		return scfinvoker.InvocationResult{}, ctx.Err()
	}
}

func TestDispatchRetrySemaphoreWaitDoesNotConsumeBoundedPassBudget(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	invoker := &deadlineGatedMarketFetchInvoker{deadlines: make(chan time.Time, 1), release: make(chan struct{})}
	scheduler := &Scheduler{Batches: db.FetchBatches(), Invoker: invoker, InvokeConcurrency: StockCNTimerInvokeConcurrency, invokeAttemptTimeout: 2 * time.Minute}
	scheduler.ensureInvokeSemaphore()
	for index := 0; index < cap(scheduler.invokeSem); index++ {
		scheduler.invokeSem <- struct{}{}
	}

	now := time.Now().UTC()
	request := Request{
		BatchID: "semaphore-wait-batch", SyncPointID: "sync-wait", ScheduleID: "retry:semaphore-wait-batch",
		BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", DatasetID: "bars", Frequency: "1m",
		Provider: "binance", SourceID: "spot_http", MarketType: "spot",
		Items: []domain.CollectionItem{{InstanceID: "wait-instance", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DatasetID: "bars", Frequency: "1m", Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}},
	}
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	deadline := now.Add(time.Minute)
	created, err := db.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{
		SpaceID: request.SpaceID, BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		Frequency: request.Frequency, Status: domain.BatchStatusPlanned, Attempt: 2, RequestJSON: string(raw),
		PlannedCount: len(request.Items), PlannedAt: &now, DeadlineAt: &deadline,
	})
	require.NoError(t, err)
	require.True(t, created)

	node := scfinvoker.Node{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}
	done := make(chan struct{})
	t.Cleanup(func() {
		invoker.unblock()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("retry dispatch goroutine did not stop during cleanup")
		}
	})
	go func() {
		defer close(done)
		scheduler.dispatchRetry(request, node, []scfinvoker.Node{node}, nil)
	}()
	select {
	case <-invoker.deadlines:
		t.Fatal("Invoke acquired a slot while all semaphore tokens were held")
	case <-time.After(2 * time.Second):
	}
	<-scheduler.invokeSem
	var invokeDeadline time.Time
	select {
	case invokeDeadline = <-invoker.deadlines:
	case <-time.After(2 * time.Second):
		t.Fatal("Invoke did not start after a semaphore slot became available")
	}
	require.Greater(t, time.Until(invokeDeadline), 80*time.Second, "queued work must retain the bounded-pass timeout rather than the old 30-second deadline")
	invoker.unblock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retry dispatch did not finish after releasing Invoke")
	}
}

func TestDispatchDueRetriesDoesNotInvokeSamePlannedBatchTwiceAcrossTicks(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, instanceID, retryKey = "crypto", "retry-instance", "retry-once"
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: spaceID, TaskID: "retry-task", DataType: "kline", Enabled: true}))
	instance := domain.TaskInstance{SpaceID: spaceID, InstanceID: instanceID, Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	target := domain.WriteTarget{ID: "retry-target", SpaceID: spaceID, InstanceID: instanceID, TaskID: "retry-task", DatasetID: "bars", Status: "pending"}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	now := time.Now().UTC()
	item := domain.CollectionItem{InstanceID: instanceID, SubjectID: instance.SubjectID, Symbol: instance.ProviderSymbol, Provider: instance.Provider, SourceID: instance.SourceID, MarketType: instance.MarketType, DataType: instance.DataType, DatasetID: target.DatasetID, Frequency: instance.Frequency, TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: spaceID, RetryKey: retryKey, SourceBatchID: "source-sync", BatchKind: domain.BatchKindRealtime,
		InstanceID: instanceID, RetryScope: "fetch", SubjectID: instance.SubjectID, Frequency: instance.Frequency,
		TargetDataTime: now.Add(-time.Minute), TaskJSON: string(itemJSON), Attempt: 1, Status: "pending", NextRetryAt: &now,
	}))

	invoker := &deadlineGatedMarketFetchInvoker{deadlines: make(chan time.Time, 2), release: make(chan struct{})}
	t.Cleanup(invoker.unblock)
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, InvokeConcurrency: 1}
	nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	select {
	case <-invoker.deadlines:
	case <-time.After(2 * time.Second):
		t.Fatal("first retry did not enter Invoke")
	}
	// A later tick sees the same pending retry and deterministic planned batch
	// while the first dispatch is still waiting for the Invoke response.
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now.Add(time.Minute)))
	invoker.unblock()
	require.Eventually(t, func() bool {
		stored, getErr := db.FetchRetries().Get(ctx, spaceID, retryKey)
		return getErr == nil && stored.Status == "dispatched"
	}, 2*time.Second, 10*time.Millisecond)
	select {
	case <-invoker.deadlines:
		t.Fatal("same deterministic retry batch was invoked twice across scheduler ticks")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDispatchRetryRejectsSupersededKeysAfterWaitingForCapacity(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, taskID = "crypto", "retry-preflight-task"
	const supersededKey, pendingKey = "retry-a-superseded", "retry-b-pending"
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: spaceID, TaskID: taskID, DataType: "kline", Enabled: true}))
	instances := []domain.TaskInstance{
		{SpaceID: spaceID, InstanceID: "instance-a", Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`},
		{SpaceID: spaceID, InstanceID: "instance-b", Provider: "binance", ProviderSymbol: "ETHUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "ETH-USDT", Frequency: "1m", TaskParams: `{}`},
	}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, instances))
	targets := []domain.WriteTarget{
		{ID: "target-a", SpaceID: spaceID, InstanceID: instances[0].InstanceID, TaskID: taskID, DatasetID: "bars", Status: "pending"},
		{ID: "target-b", SpaceID: spaceID, InstanceID: instances[1].InstanceID, TaskID: taskID, DatasetID: "bars", Status: "pending"},
	}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, targets))
	now := time.Now().UTC()
	items := []domain.CollectionItem{
		{InstanceID: instances[0].InstanceID, SubjectID: instances[0].SubjectID, Symbol: instances[0].ProviderSymbol, Provider: instances[0].Provider, SourceID: instances[0].SourceID, MarketType: instances[0].MarketType, DataType: instances[0].DataType, DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano), SourceEventID: supersededKey},
		{InstanceID: instances[1].InstanceID, SubjectID: instances[1].SubjectID, Symbol: instances[1].ProviderSymbol, Provider: instances[1].Provider, SourceID: instances[1].SourceID, MarketType: instances[1].MarketType, DataType: instances[1].DataType, DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano), SourceEventID: pendingKey},
	}
	for index, key := range []string{supersededKey, pendingKey} {
		itemRaw, err := json.Marshal(items[index])
		require.NoError(t, err)
		require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: spaceID, RetryKey: key, SourceBatchID: "retry-preflight-source", BatchKind: domain.BatchKindRealtime,
			InstanceID: items[index].InstanceID, RetryScope: "fetch", SubjectID: items[index].SubjectID, Frequency: items[index].Frequency,
			TargetDataTime: now.Add(-time.Minute), TaskJSON: string(itemRaw), Attempt: 1, Status: "pending", NextRetryAt: &now,
		}))
	}

	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, InvokeConcurrency: 1}
	scheduler.ensureInvokeSemaphore()
	scheduler.invokeSem <- struct{}{}
	nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))

	sourceItem := items[0]
	compatibilityKey := retryDispatchCompatibilityKey(domain.RetryItem{
		RetryKey: supersededKey, SourceBatchID: "retry-preflight-source", BatchKind: domain.BatchKindRealtime,
		Attempt: 1, RetryScope: "fetch",
	}, sourceItem, []domain.WriteTarget{targets[0]}, "fetch", domain.BatchKindRealtime)
	batchID := stableID(spaceID, "retry", compatibilityKey, "2", supersededKey, pendingKey)
	oldBatch, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	var oldRequest Request
	require.NoError(t, json.Unmarshal([]byte(oldBatch.RequestJSON), &oldRequest))
	require.Len(t, oldRequest.Items, 2, "the queued first plan must contain both retry keys")
	require.NoError(t, db.FetchRetries().MarkStatus(ctx, spaceID, supersededKey, "superseded"))
	<-scheduler.invokeSem

	require.Eventually(t, func() bool {
		stored, getErr := db.FetchBatches().Get(ctx, spaceID, batchID)
		return getErr == nil && stored.Status == domain.BatchStatusFailed
	}, 2*time.Second, 10*time.Millisecond)
	require.Empty(t, invoker.snapshot(), "a stale planned payload must not be invoked after semaphore wait")
	require.Eventually(t, func() bool {
		scheduler.retryDispatchMu.Lock()
		defer scheduler.retryDispatchMu.Unlock()
		return len(scheduler.retryDispatchInFlight) == 0
	}, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{items[1].SubjectID}, invokedSubjectIDs(invoker.snapshot()), "the next Tick must regroup only remaining pending work")
	stale, err := db.FetchRetries().Get(ctx, spaceID, supersededKey)
	require.NoError(t, err)
	require.Equal(t, "superseded", stale.Status)
	remaining, err := db.FetchRetries().Get(ctx, spaceID, pendingKey)
	require.NoError(t, err)
	require.Equal(t, "dispatched", remaining.Status)
}

func TestRetryDispatchInFlightKeysBlockOverlappingSubset(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, taskID = "crypto", "retry-overlap-task"
	const supersededKey, pendingKey = "retry-overlap-a", "retry-overlap-b"
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: spaceID, TaskID: taskID, DataType: "kline", Enabled: true}))
	instances := []domain.TaskInstance{
		{SpaceID: spaceID, InstanceID: "overlap-instance-a", Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`},
		{SpaceID: spaceID, InstanceID: "overlap-instance-b", Provider: "binance", ProviderSymbol: "ETHUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "ETH-USDT", Frequency: "1m", TaskParams: `{}`},
	}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, instances))
	targets := []domain.WriteTarget{
		{ID: "overlap-target-a", SpaceID: spaceID, InstanceID: instances[0].InstanceID, TaskID: taskID, DatasetID: "bars", Status: "pending"},
		{ID: "overlap-target-b", SpaceID: spaceID, InstanceID: instances[1].InstanceID, TaskID: taskID, DatasetID: "bars", Status: "pending"},
	}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, targets))
	now := time.Now().UTC()
	items := []domain.CollectionItem{
		{InstanceID: instances[0].InstanceID, SubjectID: instances[0].SubjectID, Symbol: instances[0].ProviderSymbol, Provider: instances[0].Provider, SourceID: instances[0].SourceID, MarketType: instances[0].MarketType, DataType: instances[0].DataType, DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano), SourceEventID: supersededKey},
		{InstanceID: instances[1].InstanceID, SubjectID: instances[1].SubjectID, Symbol: instances[1].ProviderSymbol, Provider: instances[1].Provider, SourceID: instances[1].SourceID, MarketType: instances[1].MarketType, DataType: instances[1].DataType, DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano), SourceEventID: pendingKey},
	}
	for index, key := range []string{supersededKey, pendingKey} {
		itemRaw, err := json.Marshal(items[index])
		require.NoError(t, err)
		require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
			SpaceID: spaceID, RetryKey: key, SourceBatchID: "retry-overlap-source", BatchKind: domain.BatchKindRealtime,
			InstanceID: items[index].InstanceID, RetryScope: "fetch", SubjectID: items[index].SubjectID, Frequency: items[index].Frequency,
			TargetDataTime: now.Add(-time.Minute), TaskJSON: string(itemRaw), Attempt: 1, Status: "pending", NextRetryAt: &now,
		}))
	}

	invoker := &deadlineGatedMarketFetchInvoker{deadlines: make(chan time.Time, 3), release: make(chan struct{})}
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, InvokeConcurrency: 2}
	t.Cleanup(func() {
		invoker.unblock()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			scheduler.retryDispatchMu.Lock()
			inFlight := len(scheduler.retryDispatchInFlight)
			scheduler.retryDispatchMu.Unlock()
			if inFlight == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("overlapping retry dispatch goroutines did not exit during cleanup")
	})
	nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	select {
	case <-invoker.deadlines:
	case <-time.After(2 * time.Second):
		t.Fatal("initial two-key retry batch did not enter Invoke")
	}
	compatibilityKey := retryDispatchCompatibilityKey(domain.RetryItem{
		RetryKey: supersededKey, SourceBatchID: "retry-overlap-source", BatchKind: domain.BatchKindRealtime, Attempt: 1, RetryScope: "fetch",
	}, items[0], []domain.WriteTarget{targets[0]}, "fetch", domain.BatchKindRealtime)
	firstBatchID := stableID(spaceID, "retry", compatibilityKey, "2", supersededKey, pendingKey)
	subsetBatchID := stableID(spaceID, "retry", pendingKey, "2")
	require.NoError(t, db.FetchRetries().MarkStatus(ctx, spaceID, supersededKey, "superseded"))

	// The remaining pending key has a different deterministic BatchID, but it
	// must remain blocked by the first batch's retry-key ownership.
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	select {
	case <-invoker.deadlines:
		t.Fatal("overlapping pending-key subset entered Invoke concurrently")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := db.FetchBatches().Get(ctx, spaceID, subsetBatchID); err == nil {
		t.Fatal("subset batch must not be persisted while its retry key is owned by an in-flight batch")
	}

	invoker.unblock()
	require.Eventually(t, func() bool {
		stored, getErr := db.FetchBatches().Get(ctx, spaceID, firstBatchID)
		return getErr == nil && stored.Status == domain.BatchStatusDispatched
	}, 2*time.Second, 10*time.Millisecond)
	for key, want := range map[string]string{supersededKey: "superseded", pendingKey: "dispatched"} {
		stored, getErr := db.FetchRetries().Get(ctx, spaceID, key)
		require.NoError(t, getErr)
		require.Equal(t, want, stored.Status)
	}
	select {
	case <-invoker.deadlines:
		t.Fatal("first accepted batch was duplicated by the overlapping subset")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRetryInvokeCandidatesExhaustedForceRecoveryOnNextTick(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, taskID, instanceID, retryKey = "crypto", "retry-failure-task", "retry-failure-instance", "retry-failure-key"
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: spaceID, TaskID: taskID, DataType: "kline", Enabled: true}))
	instance := domain.TaskInstance{SpaceID: spaceID, InstanceID: instanceID, Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	target := domain.WriteTarget{ID: "retry-failure-target", SpaceID: spaceID, InstanceID: instanceID, TaskID: taskID, DatasetID: "bars", Status: "pending"}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	now := time.Now().UTC()
	item := domain.CollectionItem{InstanceID: instanceID, SubjectID: instance.SubjectID, Symbol: instance.ProviderSymbol, Provider: instance.Provider, SourceID: instance.SourceID, MarketType: instance.MarketType, DataType: instance.DataType, DatasetID: target.DatasetID, Frequency: instance.Frequency, TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}
	itemRaw, err := json.Marshal(item)
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: spaceID, RetryKey: retryKey, SourceBatchID: "retry-failure-source", BatchKind: domain.BatchKindRealtime,
		InstanceID: instanceID, RetryScope: "fetch", SubjectID: instance.SubjectID, Frequency: instance.Frequency,
		TargetDataTime: now.Add(-time.Minute), TaskJSON: string(itemRaw), Attempt: 1, Status: "pending", NextRetryAt: &now,
	}))

	invoker := &alwaysFailMarketFetchInvoker{}
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker, InvokeConcurrency: 1}
	nodes := []scfinvoker.Node{
		{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"},
		{NodeID: "invoke-2", FunctionName: "fetch-2", Region: "ap-shanghai", TriggerType: "invoke"},
	}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	require.Eventually(t, func() bool {
		scheduler.retryDispatchMu.Lock()
		defer scheduler.retryDispatchMu.Unlock()
		return len(scheduler.retryDispatchInFlight) == 0
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, 2, invoker.count(), "both bounded invocation candidates should fail")

	batchID := stableID(spaceID, "retry", retryKey, "2")
	batch, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, batch.Status)
	require.NotNil(t, batch.DeadlineAt)
	require.True(t, !batch.DeadlineAt.After(time.Now().UTC()), "exhausted Invoke candidates must make the planned batch due for next-tick recovery")

	recoveryAt := time.Now().UTC().Add(time.Second)
	require.NoError(t, scheduler.recoverDue(ctx, spaceID, nodes, recoveryAt))
	batch, err = db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusTimedOut, batch.Status)
	recoveredRetry, err := db.FetchRetries().Get(ctx, spaceID, retryKey)
	require.NoError(t, err)
	require.Equal(t, 2, recoveredRetry.Attempt, "the failed planned attempt must advance through ordinary bounded recovery")
	require.Equal(t, "pending", recoveredRetry.Status)
}

func TestPlannedRetryDeadlineCoversQueueBudgetUntilInvokeDispatch(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, taskID, instanceID, retryKey = "crypto", "deadline-task", "deadline-instance", "deadline-retry"
	require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{SpaceID: spaceID, TaskID: taskID, DataType: "kline", Enabled: true}))
	instance := domain.TaskInstance{SpaceID: spaceID, InstanceID: instanceID, Provider: "binance", ProviderSymbol: "BTCUSDT", SourceID: "spot_http", MarketType: "spot", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m", TaskParams: `{}`}
	require.NoError(t, db.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	target := domain.WriteTarget{ID: "deadline-target", SpaceID: spaceID, InstanceID: instanceID, TaskID: taskID, DatasetID: "bars", Status: "pending"}
	require.NoError(t, db.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{target}))
	dispatchStartedAt := time.Now().UTC()
	now := dispatchStartedAt.Add(-5 * time.Minute).Truncate(time.Second)
	item := domain.CollectionItem{InstanceID: instanceID, SubjectID: instance.SubjectID, Symbol: instance.ProviderSymbol, Provider: instance.Provider, SourceID: instance.SourceID, MarketType: instance.MarketType, DataType: instance.DataType, DatasetID: target.DatasetID, Frequency: instance.Frequency, TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}
	itemJSON, err := json.Marshal(item)
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: spaceID, RetryKey: retryKey, SourceBatchID: "deadline-sync", BatchKind: domain.BatchKindRealtime,
		InstanceID: instanceID, RetryScope: "fetch", SubjectID: instance.SubjectID, Frequency: instance.Frequency,
		TargetDataTime: now.Add(-time.Minute), TaskJSON: string(itemJSON), Attempt: 1, Status: "pending", NextRetryAt: &now,
	}))
	batchID := stableID(spaceID, "retry", retryKey, "2")
	queuedRequest := Request{
		BatchID: batchID, SyncPointID: "deadline-sync", ScheduleID: "retry:" + batchID, BatchKind: domain.BatchKindRealtime,
		SpaceID: spaceID, DatasetID: target.DatasetID, Frequency: instance.Frequency, Provider: instance.Provider,
		SourceID: instance.SourceID, MarketType: instance.MarketType, Region: "ap-hongkong", NodeID: "invoke-1", FunctionName: "fetch-1",
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target},
	}
	queuedRaw, err := json.Marshal(queuedRequest)
	require.NoError(t, err)
	oldQueueDeadline := dispatchStartedAt.Add(10 * time.Second)
	created, err := db.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{
		SpaceID: spaceID, BatchID: batchID, ScheduleID: queuedRequest.ScheduleID, BatchKind: queuedRequest.BatchKind,
		Frequency: instance.Frequency, Region: queuedRequest.Region, NodeID: queuedRequest.NodeID, FunctionName: queuedRequest.FunctionName,
		Status: domain.BatchStatusPlanned, Attempt: 2, RequestJSON: string(queuedRaw), PlannedAt: &dispatchStartedAt, DeadlineAt: &oldQueueDeadline,
	})
	require.NoError(t, err)
	require.True(t, created)

	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{Instances: db.TaskInstances(), Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker}
	scheduler.ensureInvokeSemaphore()
	require.Equal(t, DefaultInvokeConcurrency, cap(scheduler.invokeSem), "this regression covers the default 20-slot queue")
	for index := 0; index < cap(scheduler.invokeSem); index++ {
		scheduler.invokeSem <- struct{}{}
	}
	var releaseSlot sync.Once
	unblockRetry := func() { releaseSlot.Do(func() { <-scheduler.invokeSem }) }
	t.Cleanup(func() {
		unblockRetry()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			scheduler.retryDispatchMu.Lock()
			inFlight := len(scheduler.retryDispatchInFlight)
			scheduler.retryDispatchMu.Unlock()
			if inFlight == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("retry dispatch goroutine did not exit during cleanup")
	})
	nodes := []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}
	require.NoError(t, scheduler.dispatchDueRetries(ctx, spaceID, nodes, now))
	planned, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, planned.Status)
	require.True(t, planned.DeadlineAt.After(oldQueueDeadline), "re-dispatch must extend an existing planned batch's nearly-expired queue deadline")
	deadlineCoversQueueBudget := planned.DeadlineAt.After(dispatchStartedAt.Add(retryDispatchWaitTimeout(DefaultInvokeConcurrency)))

	require.NoError(t, scheduler.recoverDue(ctx, spaceID, nodes, dispatchStartedAt.Add(defaultBatchCompletionDeadline)))
	stillPlanned, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, stillPlanned.Status, "queue wait must not be mistaken for an SCF completion timeout")
	storedRetry, err := db.FetchRetries().Get(ctx, spaceID, retryKey)
	require.NoError(t, err)
	require.Equal(t, "pending", storedRetry.Status)
	require.Equal(t, 1, storedRetry.Attempt, "recoverDue must not consume an attempt before Invoke starts")
	require.True(t, deadlineCoversQueueBudget, "planned retry deadline must be anchored to actual batch creation, not Tick's stale time")

	unblockRetry()
	require.Eventually(t, func() bool {
		dispatched, getErr := db.FetchBatches().Get(ctx, spaceID, batchID)
		return getErr == nil && dispatched.Status == domain.BatchStatusDispatched
	}, 2*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		scheduler.retryDispatchMu.Lock()
		defer scheduler.retryDispatchMu.Unlock()
		return len(scheduler.retryDispatchInFlight) == 0
	}, 2*time.Second, 10*time.Millisecond)
	dispatched, err := db.FetchBatches().Get(ctx, spaceID, batchID)
	require.NoError(t, err)
	require.Greater(t, dispatched.DeadlineAt.Sub(time.Now().UTC()), 60*time.Second, "successful Invoke must replace queue deadline with the 70-second completion deadline")
}

func TestDispatchRetrySemaphoreTimeoutLeavesRetryQueued(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	const spaceID, instanceID, retryKey = "crypto", "queue-timeout-instance", "queue-timeout-retry"
	now := time.Now().UTC()
	item := domain.CollectionItem{InstanceID: instanceID, SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Provider: "binance", SourceID: "spot_http", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m", TargetDataTime: now.Add(-time.Minute).Format(time.RFC3339Nano)}
	itemRaw, err := json.Marshal(item)
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: spaceID, RetryKey: retryKey, SourceBatchID: "queue-timeout-source", BatchKind: domain.BatchKindRealtime,
		InstanceID: instanceID, SubjectID: item.SubjectID, Frequency: item.Frequency, TargetDataTime: now.Add(-time.Minute),
		TaskJSON: string(itemRaw), Attempt: 1, Status: "pending", NextRetryAt: &now,
	}))
	request := Request{BatchID: "queue-timeout-batch", ScheduleID: "retry:queue-timeout-batch", BatchKind: domain.BatchKindRealtime, SpaceID: spaceID, DatasetID: "bars", Frequency: "1m", Provider: "binance", SourceID: "spot_http", MarketType: "spot", Region: "ap-hongkong", NodeID: "invoke-1", Items: []domain.CollectionItem{item}}
	requestRaw, err := json.Marshal(request)
	require.NoError(t, err)
	deadline := now.Add(time.Hour)
	created, err := db.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{
		SpaceID: spaceID, BatchID: request.BatchID, ScheduleID: request.ScheduleID, BatchKind: request.BatchKind,
		Frequency: request.Frequency, Status: domain.BatchStatusPlanned, Attempt: 2, RequestJSON: string(requestRaw),
		PlannedCount: 1, PlannedAt: &now, DeadlineAt: &deadline,
	})
	require.NoError(t, err)
	require.True(t, created)

	invoker := &recordingMarketFetchInvoker{}
	scheduler := &Scheduler{
		Batches: db.FetchBatches(), Retries: db.FetchRetries(), Invoker: invoker,
		InvokeConcurrency: 1, retryDispatchWaitTimeoutOverride: 25 * time.Millisecond,
	}
	scheduler.ensureInvokeSemaphore()
	scheduler.invokeSem <- struct{}{}
	done := make(chan struct{})
	node := scfinvoker.Node{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}
	go func() {
		defer close(done)
		scheduler.dispatchRetry(request, node, []scfinvoker.Node{node}, []string{retryKey})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bounded semaphore wait did not time out")
	}
	require.Empty(t, invoker.snapshot(), "queue timeout must not invoke SCF")
	storedBatch, err := db.FetchBatches().Get(ctx, spaceID, request.BatchID)
	require.NoError(t, err)
	require.Equal(t, domain.BatchStatusPlanned, storedBatch.Status, "queue timeout must preserve the planned batch")
	storedRetry, err := db.FetchRetries().Get(ctx, spaceID, retryKey)
	require.NoError(t, err)
	require.Equal(t, "pending", storedRetry.Status, "queue timeout must not consume or dispatch retry work")
}

func TestTickInvokesPriorityCryptoMinuteWhenTimersOwnRealtime(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_kline", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
		Enabled: true,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	invoker := &recordingMarketFetchInvoker{
		timerNodes: []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "fn-timer-1", Region: "ap-hongkong", TriggerType: "timer"}},
	}
	now := time.Date(2026, 9, 15, 3, 12, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks:                 db.Tasks(),
		Instances:             db.TaskInstances(),
		Batches:               db.FetchBatches(),
		Invoker:               invoker,
		InvokeNonRealtimeOnly: true,
		SpaceID:               "crypto",
		Now:                   func() time.Time { return now },
		ResolveSourceID:       testSourceID,
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "AAA-USDT", Status: "active"},
			{SubjectID: "BTC-USDT", Status: "active"},
			{SubjectID: "ETH-USDT", Status: "active"},
		}},
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 2 }, 2*time.Second, 10*time.Millisecond)
	require.ElementsMatch(t, []string{"BTC-USDT", "ETH-USDT"}, invokedSubjectIDs(invoker.snapshot()))
	for _, invoke := range invoker.snapshot() {
		require.Equal(t, "timer-1", invoke.nodeID)
		require.Equal(t, "market_fetch", invoke.event["action"])
		require.Empty(t, invoke.event["Type"])
		data, ok := invoke.event["data"].(map[string]any)
		require.True(t, ok)
		items, ok := data["items"].([]any)
		require.True(t, ok)
		require.Len(t, items, 1)
		item, ok := items[0].(map[string]any)
		require.True(t, ok)
		require.EqualValues(t, 2, item["bar_limit"])
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Never(t, func() bool { return len(invoker.snapshot()) > 2 }, 200*time.Millisecond, 20*time.Millisecond)
}

func TestTickDoesNotDuplicatePriorityWhenInvokeOwnsRealtime(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_kline", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
		Enabled: true,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	invoker := &recordingMarketFetchInvoker{
		invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}},
		timerNodes:  []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "fn-timer-1", Region: "ap-hongkong", TriggerType: "timer"}},
	}
	now := time.Date(2026, 9, 15, 3, 12, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks:           db.Tasks(),
		Instances:       db.TaskInstances(),
		Batches:         db.FetchBatches(),
		Invoker:         invoker,
		SpaceID:         "crypto",
		Now:             func() time.Time { return now },
		ResolveSourceID: testSourceID,
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "AAA-USDT", Status: "active"},
			{SubjectID: "BTC-USDT", Status: "active"},
			{SubjectID: "ETH-USDT", Status: "active"},
		}},
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) > 0 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, []string{"AAA-USDT", "BTC-USDT", "ETH-USDT"}, invokedSubjectIDs(invoker.snapshot()))
	for _, invoke := range invoker.snapshot() {
		require.Equal(t, "invoke-1", invoke.nodeID)
	}
}

func TestTickPrefersInvokeNodesForPriorityCryptoMinute(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_kline", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
		Enabled: true,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	invoker := &recordingMarketFetchInvoker{
		invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}},
		timerNodes:  []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "fn-timer-1", Region: "ap-hongkong", TriggerType: "timer"}},
	}
	now := time.Date(2026, 9, 15, 3, 12, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks:                 db.Tasks(),
		Instances:             db.TaskInstances(),
		Batches:               db.FetchBatches(),
		Invoker:               invoker,
		InvokeNonRealtimeOnly: true,
		SpaceID:               "crypto",
		Now:                   func() time.Time { return now },
		ResolveSourceID:       testSourceID,
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "BTC-USDT", Status: "active"},
		}},
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, "invoke-1", invoker.snapshot()[0].nodeID)
	require.Equal(t, []string{"BTC-USDT"}, invokedSubjectIDs(invoker.snapshot()))
}

func TestExpandTaskSkipsMalformedSnapshotSubjects(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID:         "crypto",
		ResolveSourceID: testSourceID,
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "BTC-USDT", Status: "active"},
			{SubjectID: "币安 人生-USDT", Status: "active"},
			{SubjectID: "BTCUSDT", Status: "active"},
		}},
	}
	items, _, err := scheduler.expandTask(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`,
	})
	assert.NoError(t, err)
	if assert.Len(t, items, 1) {
		assert.Equal(t, "BTC-USDT", items[0].SubjectID)
	}
}

func TestSchedulerTickAdvancesRunLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invokeNode []scfinvoker.Node
		wantErr    string
		wantStatus domain.RunStatus
	}{
		{name: "planned work becomes active", invokeNode: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}}, wantStatus: domain.RunStatusActive},
		{name: "planning failure marks run failed", wantErr: "no active Invoke market fetcher nodes", wantStatus: domain.RunStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestMarketFetchStore(t)
			ctx := context.Background()
			task := domain.CollectionTask{
				SpaceID: "crypto", TaskID: "bars", TaskName: "Bars", DataType: "kline", Enabled: true,
				CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"bars","frequency":"1m"}`,
			}
			require.NoError(t, db.Tasks().Create(ctx, task))
			now := time.Date(2026, 9, 27, 12, 34, 45, 0, time.UTC)
			invoker := &recordingMarketFetchInvoker{invokeNodes: tc.invokeNode}
			scheduler := &Scheduler{
				Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
				Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return now }, ResolveSourceID: testSourceID,
				Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
			}
			err := scheduler.Tick(ctx, "crypto")
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			runKey := fmt.Sprintf("scheduled:%s", now.Truncate(time.Minute).Format(time.RFC3339))
			run, getErr := db.Runs().GetOrCreateScheduled(ctx, "crypto", runKey, "scheduled", "1m", now.Truncate(time.Minute))
			require.NoError(t, getErr)
			require.Equal(t, tc.wantStatus, run.Status)
			if tc.wantStatus == domain.RunStatusFailed {
				require.Contains(t, run.ErrorSummary, tc.wantErr)
			}
		})
	}
}

func TestSchedulerSkipsUnchangedHourlyTargetInNextMinuteRun(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "bars-1m", TaskName: "Minute bars", DataType: "kline", Enabled: true, CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"bars-1m","frequency":"1m"}`},
		{SpaceID: "crypto", TaskID: "bars-1h", TaskName: "Hourly bars", DataType: "kline", Enabled: true, CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"bars-1h","frequency":"1H"}`},
	} {
		require.NoError(t, db.Tasks().Create(ctx, task))
	}
	invoker := &recordingMarketFetchInvoker{invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}}}
	now := time.Date(2026, 9, 28, 18, 5, 30, 0, time.UTC)
	scheduler := &Scheduler{
		Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
		Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return now }, ResolveSourceID: testSourceID,
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}},
	}

	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	firstKey := fmt.Sprintf("scheduled:%s", now.Truncate(time.Minute).Format(time.RFC3339))
	firstRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", firstKey, "scheduled", "1m", now.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err := db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 2, countInstancesForRun(instances, firstRun.RunID))

	now = now.Add(time.Minute)
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	secondKey := fmt.Sprintf("scheduled:%s", now.Truncate(time.Minute).Format(time.RFC3339))
	secondRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", secondKey, "scheduled", "1m", now.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err = db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countInstancesForRun(instances, secondRun.RunID), "unchanged 1H target must not create a new pending instance every minute")
	for _, instance := range instances {
		if instance.RunID == secondRun.RunID {
			require.Equal(t, "1m", instance.Frequency)
		}
	}

	now = time.Date(2026, 9, 28, 19, 0, 30, 0, time.UTC)
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	thirdKey := fmt.Sprintf("scheduled:%s", now.Truncate(time.Minute).Format(time.RFC3339))
	thirdRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", thirdKey, "scheduled", "1m", now.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err = db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 2, countInstancesForRun(instances, thirdRun.RunID), "new closed 1H target must be scheduled on the hour")
}

func TestSchedulerRunCutoffDefersNewTaskToNextRun(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	first := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars-existing", TaskName: "Existing bars", DataType: "kline", Enabled: true, DefinitionHash: "definition-existing",
		CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"bars-existing","frequency":"1m"}`,
	}
	require.NoError(t, db.Tasks().Create(ctx, first))
	source := &mutableDatasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}}
	invoker := &recordingMarketFetchInvoker{invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}}}
	now := time.Date(2026, 9, 28, 6, 20, 30, 0, time.UTC)
	newScheduler := func(at time.Time) *Scheduler {
		return &Scheduler{
			Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
			Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return at }, ResolveSourceID: testSourceID, Symbols: source,
		}
	}

	require.NoError(t, newScheduler(now).Tick(ctx, "crypto"))
	firstKey := fmt.Sprintf("scheduled:%s", now.Truncate(time.Minute).Format(time.RFC3339))
	firstRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", firstKey, "scheduled", "1m", now.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err := db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countInstancesForRun(instances, firstRun.RunID))

	// The second task is created after the scheduled Run already owns its
	// immutable cutoff. Its different market route would create another
	// Provider request if it were incorrectly appended to that Run.
	time.Sleep(2 * time.Millisecond)
	late := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars-late", TaskName: "Late bars", DataType: "kline", Enabled: true, DefinitionHash: "definition-late",
		CollectParams: `{"provider":"binance","market_type":"swap","target_dataset_id":"bars-late","frequency":"1m"}`,
	}
	require.NoError(t, db.Tasks().Create(ctx, late))
	storedLate, err := db.Tasks().GetByTaskID(ctx, "crypto", late.TaskID)
	require.NoError(t, err)
	require.True(t, storedLate.CreateTime.After(firstRun.CreateTime), "test fixture must create the task after the Run cutoff")

	require.NoError(t, newScheduler(now).Tick(ctx, "crypto"))
	instances, err = db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countInstancesForRun(instances, firstRun.RunID), "cutoff-late task must not be appended to the existing Run")

	nextNow := now.Add(time.Minute)
	require.NoError(t, newScheduler(nextNow).Tick(ctx, "crypto"))
	nextKey := fmt.Sprintf("scheduled:%s", nextNow.Truncate(time.Minute).Format(time.RFC3339))
	nextRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", nextKey, "scheduled", "1m", nextNow.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err = db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 2, countInstancesForRun(instances, nextRun.RunID), "the next Run must include both spot and newly-created swap requests")
}

func TestSchedulerRunCutoffDefersSeriesChangeToNextRun(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars-cutoff", TaskName: "Bars cutoff", DataType: "kline", Enabled: true, DefinitionHash: "definition-v1",
		CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"bars","frequency":"1m"}`,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	source := &mutableDatasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}}}
	invoker := &recordingMarketFetchInvoker{invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}}}
	now := time.Date(2026, 9, 28, 6, 10, 30, 0, time.UTC)
	newScheduler := func(at time.Time) *Scheduler {
		return &Scheduler{
			Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
			Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return at }, ResolveSourceID: testSourceID, Symbols: source,
		}
	}

	require.NoError(t, newScheduler(now).Tick(ctx, "crypto"))
	firstKey := fmt.Sprintf("scheduled:%s", now.Truncate(time.Minute).Format(time.RFC3339))
	firstRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", firstKey, "scheduled", "1m", now.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err := db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countInstancesForRun(instances, firstRun.RunID))
	before, err := db.Tasks().GetByTaskID(ctx, "crypto", task.TaskID)
	require.NoError(t, err)
	require.NotEmpty(t, before.SeriesHash)

	source.SetSubjects([]domain.DatasetSubject{{SubjectID: "BTC-USDT", Status: "active"}, {SubjectID: "ETH-USDT", Status: "active"}})
	require.NoError(t, newScheduler(now).Tick(ctx, "crypto"), "same-minute scheduler must refresh membership but not append it to the existing Run")
	instances, err = db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 1, countInstancesForRun(instances, firstRun.RunID))
	afterRefresh, err := db.Tasks().GetByTaskID(ctx, "crypto", task.TaskID)
	require.NoError(t, err)
	require.NotEqual(t, before.SeriesHash, afterRefresh.SeriesHash)
	require.True(t, afterRefresh.ModifyTime.After(firstRun.CreateTime) || afterRefresh.ModifyTime.Equal(firstRun.CreateTime))

	nextNow := now.Add(time.Minute)
	require.NoError(t, newScheduler(nextNow).Tick(ctx, "crypto"))
	nextKey := fmt.Sprintf("scheduled:%s", nextNow.Truncate(time.Minute).Format(time.RFC3339))
	nextRun, err := db.Runs().GetOrCreateScheduled(ctx, "crypto", nextKey, "scheduled", "1m", nextNow.Truncate(time.Minute))
	require.NoError(t, err)
	instances, err = db.TaskInstances().ListAll(ctx, "crypto", 100)
	require.NoError(t, err)
	require.Equal(t, 2, countInstancesForRun(instances, nextRun.RunID))
}

func TestSchedulerReusesPeriodSeriesSnapshotAfterTagMembershipChangesAndRestart(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars-period-snapshot", TaskName: "Period series snapshot", DataType: "kline",
		TagIDs: []string{"binance-spot"}, CollectParams: `{"target_dataset_id":"bars","frequency":"1h"}`, Enabled: true,
	}
	require.NoError(t, db.Tasks().Create(ctx, task))
	source := &countingTagDatasetSource{
		tag:      &storagepb.Tag{SpaceId: "crypto", TagId: "binance-spot", Source: "binance", MarketType: "spot"},
		subjects: []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}, {SubjectID: "ETH-USDT", Status: "active"}},
	}
	invoker := &recordingMarketFetchInvoker{invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fetch-1", Region: "ap-hongkong", TriggerType: "invoke"}}}
	now := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	newScheduler := func(at time.Time) *Scheduler {
		return &Scheduler{
			Tasks: db.Tasks(), Instances: db.TaskInstances(), Batches: db.FetchBatches(), Runs: db.Runs(), Retries: db.FetchRetries(),
			PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), Invoker: invoker, SpaceID: "crypto", Now: func() time.Time { return at },
			ResolveSourceID: testSourceID, Symbols: source,
		}
	}

	require.NoError(t, newScheduler(now).Tick(ctx, "crypto"))
	firstPeriod, err := targetDataTime(now, "1h")
	require.NoError(t, err)
	firstSnapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1H", PeriodTime: firstPeriod})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(2), firstSnapshot.ExpectedCount)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, periodSeriesSubjectsForScheduler(firstSnapshot))
	require.Equal(t, 1, source.resolveSubjectsCalls)

	source.subjects = []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}}
	// A new scheduler instance models process restart. The period snapshot is
	// persistent, so current Tag members are not resolved for this same period.
	require.NoError(t, newScheduler(now.Add(time.Minute)).Tick(ctx, "crypto"))
	loadedSnapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, firstSnapshot.Key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, firstSnapshot, loadedSnapshot)
	require.Equal(t, 1, source.resolveSubjectsCalls, "existing periods must not re-resolve Storage tag members")

	nextNow := now.Add(time.Hour)
	require.NoError(t, newScheduler(nextNow).Tick(ctx, "crypto"))
	nextPeriod, err := targetDataTime(nextNow, "1h")
	require.NoError(t, err)
	require.NotEqual(t, firstPeriod, nextPeriod)
	nextSnapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1H", PeriodTime: nextPeriod})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(1), nextSnapshot.ExpectedCount)
	require.Equal(t, []string{"BTC-USDT"}, periodSeriesSubjectsForScheduler(nextSnapshot))
	require.Equal(t, 2, source.resolveSubjectsCalls, "the next period must use the refreshed Storage Tag members")
}

func periodSeriesSubjectsForScheduler(snapshot domain.PeriodSeriesSnapshot) []string {
	subjects := make([]string, 0, len(snapshot.Entries))
	for _, row := range snapshot.Entries {
		subjects = append(subjects, row.SubjectID)
	}
	return subjects
}

type datasetSourceStub struct {
	subjects []domain.DatasetSubject
}

func (s datasetSourceStub) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "symbols"}, nil
}

func (s datasetSourceStub) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	return append([]domain.DatasetSubject(nil), s.subjects...), nil
}

func (s datasetSourceStub) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	items := make([]domain.Subject, 0, len(s.subjects))
	for _, subject := range s.subjects {
		items = append(items, domain.Subject{SubjectID: subject.SubjectID, Name: subject.SubjectName, Status: subject.Status})
	}
	return items, nil
}

type countingTagDatasetSource struct {
	tag                  *storagepb.Tag
	subjects             []domain.Subject
	getDatasetCalls      int
	getTagCalls          int
	resolveSubjectsCalls int
}

func (s *countingTagDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	s.getDatasetCalls++
	return storagesource.DatasetInfo{DataSourceID: "symbols"}, nil
}

func (s *countingTagDatasetSource) GetTag(context.Context, string, string) (*storagepb.Tag, error) {
	s.getTagCalls++
	return s.tag, nil
}

func (s *countingTagDatasetSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	s.resolveSubjectsCalls++
	return append([]domain.Subject(nil), s.subjects...), nil
}

type tagAwareDatasetSourceStub struct {
	tags     map[string]*storagepb.Tag
	subjects map[string][]domain.Subject
}

func (s tagAwareDatasetSourceStub) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "symbols"}, nil
}

func (s tagAwareDatasetSourceStub) GetTag(_ context.Context, _ string, tagID string) (*storagepb.Tag, error) {
	if tag := s.tags[tagID]; tag != nil {
		return tag, nil
	}
	return nil, fmt.Errorf("tag %s not found", tagID)
}

func (s tagAwareDatasetSourceStub) ResolveSubjects(_ context.Context, _ string, tagIDs []string) ([]domain.Subject, error) {
	var result []domain.Subject
	for _, tagID := range tagIDs {
		result = append(result, s.subjects[tagID]...)
	}
	return result, nil
}

type mutableDatasetSourceStub struct {
	mu       sync.RWMutex
	subjects []domain.DatasetSubject
}

func (s *mutableDatasetSourceStub) SetSubjects(subjects []domain.DatasetSubject) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subjects = append([]domain.DatasetSubject(nil), subjects...)
}

func (s *mutableDatasetSourceStub) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "symbols"}, nil
}

func (s *mutableDatasetSourceStub) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Subject, 0, len(s.subjects))
	for _, subject := range s.subjects {
		items = append(items, domain.Subject{SubjectID: subject.SubjectID, Name: subject.SubjectName, Status: subject.Status})
	}
	return items, nil
}

func countInstancesForRun(instances []domain.TaskInstance, runID string) int {
	count := 0
	for _, instance := range instances {
		if instance.RunID == runID {
			count++
		}
	}
	return count
}

func targetDataTimeForTest(now time.Time, frequency string) time.Time {
	target, err := targetDataTime(now, frequency)
	if err != nil {
		panic(err)
	}
	return target.UTC()
}

func testSourceID(provider, instrumentType string) string {
	if strings.EqualFold(provider, "binance") {
		switch strings.ToLower(strings.TrimSpace(instrumentType)) {
		case "spot":
			return "spot_http"
		case "swap":
			return "swap_http"
		}
	}
	return ""
}

func taskIDs(tasks []domain.CollectionTask) []string {
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		ids = append(ids, task.TaskID)
	}
	return ids
}

type recordedInvoke struct {
	nodeID string
	event  map[string]any
}

type recordingMarketFetchInvoker struct {
	mu          sync.Mutex
	invokeNodes []scfinvoker.Node
	timerNodes  []scfinvoker.Node
	invokes     []recordedInvoke
	err         error
}

type alwaysFailMarketFetchInvoker struct {
	mu    sync.Mutex
	calls int
}

func (r *alwaysFailMarketFetchInvoker) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (r *alwaysFailMarketFetchInvoker) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return nil, nil
}

func (r *alwaysFailMarketFetchInvoker) Invoke(context.Context, string, string, map[string]any, cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return scfinvoker.InvocationResult{}, fmt.Errorf("SCF invoke unavailable")
}

func (r *alwaysFailMarketFetchInvoker) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *recordingMarketFetchInvoker) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]scfinvoker.Node(nil), r.invokeNodes...), nil
}

func (r *recordingMarketFetchInvoker) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]scfinvoker.Node(nil), r.timerNodes...), nil
}

func (r *recordingMarketFetchInvoker) Invoke(_ context.Context, _, nodeID string, event map[string]any, _ cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invokes = append(r.invokes, recordedInvoke{nodeID: nodeID, event: event})
	return scfinvoker.InvocationResult{RequestID: "req-1"}, nil
}

func (r *recordingMarketFetchInvoker) snapshot() []recordedInvoke {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedInvoke(nil), r.invokes...)
}

func invokedSubjectIDs(invokes []recordedInvoke) []string {
	ids := make([]string, 0, len(invokes))
	for _, invoke := range invokes {
		data, _ := invoke.event["data"].(map[string]any)
		items, _ := data["items"].([]any)
		for _, item := range items {
			fields, _ := item.(map[string]any)
			ids = append(ids, strings.TrimSpace(fmt.Sprint(fields["subject_id"])))
		}
	}
	return ids
}

type recordingPeriodFailureStorage struct {
	calls        int
	expectations []*storagepb.DatasetPeriodExpectation
	indexes      [][]uint32
	ensured      []*storagepb.DatasetPeriodExpectation
}

func (s *recordingPeriodFailureStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}
func (s *recordingPeriodFailureStorage) EnsureDatasetPeriod(_ context.Context, exp *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.ensured = append(s.ensured, exp)
	return testPeriodStorageStateFromExpectation(exp), nil
}
func (s *recordingPeriodFailureStorage) GetDatasetPeriodStatus(_ context.Context, exp *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	return testPeriodStorageStateFromExpectation(exp), nil
}
func (s *recordingPeriodFailureStorage) CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error {
	return nil
}
func (s *recordingPeriodFailureStorage) RecordDatasetPeriodFailures(_ context.Context, exp *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
	s.calls++
	s.expectations = append(s.expectations, exp)
	s.indexes = append(s.indexes, append([]uint32(nil), indexes...))
	return recordedFailureResults(indexes, storagepb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED), nil
}

func TestSchedulerEnsurePeriodUsesPersistedSeriesTags(t *testing.T) {
	for _, space := range []string{"crypto", StockCNSpaceID} {
		t.Run(space, func(t *testing.T) {
			db := newTestMarketFetchStore(t)
			ctx := context.Background()
			period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			task := domain.CollectionTask{SpaceID: space, TaskID: "series-tag-task", DataType: "kline", CollectParams: `{"target_dataset_id":"bars","frequency":"1m"}`}
			items := []domain.CollectionItem{{SubjectID: "BTC-USDT", DatasetID: "bars", Provider: "okx", SourceID: "okx", MarketType: "spot", Symbol: "BTC-USDT"}, {SubjectID: "BTC-USDT", DatasetID: "bars", Provider: "binance", SourceID: "binance", MarketType: "spot", Symbol: "BTCUSDT"}}
			if space == StockCNSpaceID {
				items = []domain.CollectionItem{{SubjectID: "600000.XSHG", DatasetID: "bars", Provider: "sina", SourceID: "stockcn", MarketType: "equity", Symbol: "sh600000"}}
			}
			storage := &recordingPeriodFailureStorage{}
			scheduler := &Scheduler{PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), PeriodStorageStates: db.PeriodStorageStates(), StorageTarget: "storage.local:11003", Storage: func(string, string, string) (Storage, error) { return storage, nil }}
			materialized, _, err := scheduler.materializeTaskSeries(ctx, task, items)
			require.NoError(t, err)
			snapshot, err := periodSeriesSnapshotFromItems(task, "bars", "1m", period, materialized)
			require.NoError(t, err)
			_, _, err = db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
			require.NoError(t, err)
			loaded, err := scheduler.expandTaskForPeriod(ctx, task, "1m", period)
			require.NoError(t, err)
			require.NoError(t, scheduler.ensureDatasetPeriod(ctx, task, loaded, "1m", period, period.Add(time.Minute)))
			require.Len(t, storage.ensured, 1)
			expectation := storage.ensured[0]
			persisted, found, err := db.PeriodStorageStates().GetPeriodStorageState(ctx, domain.PeriodKey{SpaceID: space, DatasetID: "bars", Frequency: "1m", PeriodTime: period})
			require.NoError(t, err)
			require.True(t, found, "a successful Ensure response is persisted as authoritative Storage state")
			require.Equal(t, expectation.GetSeriesHash(), persisted.SeriesHash)
			require.Equal(t, expectation.GetExpectedCount(), persisted.ExpectedCount)
			require.Equal(t, time.Unix(expectation.GetDeadlineAt(), 0).UTC(), persisted.DeadlineAt)
			require.Equal(t, snapshot.ExpectedCount, expectation.GetExpectedCount())
			require.Equal(t, snapshot.SeriesHash, expectation.GetSeriesHash())
			require.Len(t, expectation.GetSeriesSnapshot(), len(snapshot.Entries))
			for index, series := range expectation.GetSeriesSnapshot() {
				require.Equal(t, uint32(index), series.GetSeriesIndex())
				require.Equal(t, snapshot.Entries[index].SubjectID, series.GetSubjectId())
				require.Equal(t, snapshot.Entries[index].SeriesTag, series.GetSeriesTag())
				require.Equal(t, collectionItemSeriesTag(space, loaded[index]), series.GetSeriesTag(), "Ensure and the final RowKey must use the same series tag")
			}
			if space == StockCNSpaceID {
				require.Equal(t, uint32(1), expectation.GetExpectedCount(), "fallback providers are one logical default series, not new expected indexes")
				require.Equal(t, "default", expectation.GetSeriesSnapshot()[0].GetSeriesTag())
				for _, provider := range []string{"sina", "tencent", "tdx", "eastmoney"} {
					fallback := loaded[0]
					fallback.Provider = provider
					require.Equal(t, "default", collectionItemSeriesTag(space, fallback))
				}
			} else {
				require.NotEqual(t, expectation.GetSeriesSnapshot()[0].GetSeriesTag(), expectation.GetSeriesSnapshot()[1].GetSeriesTag())
			}
		})
	}
}

func TestMonthlyPeriodFrequencyIsPreservedAcrossCollectorContracts(t *testing.T) {
	assertPeriodFrequencyIsPreservedAcrossCollectorContracts(t, "1M", "1M", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), func(start time.Time) time.Time {
		return start.AddDate(0, 1, 0)
	})
}

func TestHourlyPeriodFrequencyIsPreservedAcrossCollectorContracts(t *testing.T) {
	for _, providerFrequency := range []string{"1H", "1h"} {
		t.Run(providerFrequency, func(t *testing.T) {
			assertPeriodFrequencyIsPreservedAcrossCollectorContracts(t, "1H", providerFrequency, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), func(start time.Time) time.Time {
				return start.Add(time.Hour)
			})
		})
	}
}

func assertPeriodFrequencyIsPreservedAcrossCollectorContracts(t *testing.T, frequency, providerFrequency string, period time.Time, periodEnd func(time.Time) time.Time) {
	ctx := context.Background()
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "period-frequency-task", DataType: "kline", CollectParams: fmt.Sprintf(`{"target_dataset_id":"bars","frequency":%q}`, frequency)}
	item := domain.CollectionItem{SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DatasetID: "bars", Provider: "binance", SourceID: "binance_http", MarketType: "spot", DataType: "kline", Frequency: frequency, TargetDataTime: period.Format(time.RFC3339Nano)}
	db := newTestMarketFetchStore(t)
	storage := &recordingPeriodFailureStorage{}
	scheduler := &Scheduler{PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), PeriodStorageStates: db.PeriodStorageStates(), StorageTarget: "storage.local:11003", Storage: func(string, string, string) (Storage, error) { return storage, nil }}
	materialized, _, err := scheduler.materializeTaskSeries(ctx, task, []domain.CollectionItem{item})
	require.NoError(t, err)
	snapshot, err := periodSeriesSnapshotFromItems(task, "bars", frequency, period, materialized)
	require.NoError(t, err)
	require.Equal(t, frequency, snapshot.Key.Frequency)
	require.Equal(t, frequency, snapshot.Entries[0].Frequency)
	_, _, err = db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, snapshot)
	require.NoError(t, err)
	loaded, err := scheduler.expandTaskForPeriod(ctx, task, frequency, period)
	require.NoError(t, err)
	require.NoError(t, scheduler.ensureDatasetPeriod(ctx, task, loaded, frequency, period, period.Add(time.Minute)))
	require.Equal(t, frequency, storage.ensured[0].GetFrequency())
	for index := range loaded {
		loaded[index] = prepareCollectionItemRequest(loaded[index], frequency, period, MaxRealtimeRows)
	}

	end := periodEnd(period)
	bar := marketdata.NormalizedKline{SubjectID: "BTC-USDT", ProviderID: "binance", SourceID: item.SourceID, ProviderSymbol: item.Symbol, Frequency: providerFrequency, BarStart: period, BarEnd: end, Open: 100, High: 101, Low: 99, Close: 100.5, VolumeShares: 10, AmountCNY: 1005, ProviderTimestamp: end, FetchedAt: end, RequestID: "period-frequency-binding"}
	pipeline := &KlinePipeline{MarketID: "crypto", InstrumentType: marketdata.InstrumentSpot, DatasetID: "bars", SourceID: item.SourceID}
	req := Request{SpaceID: "crypto", DatasetID: "bars", Frequency: frequency, SourceID: item.SourceID, MarketType: "spot", Items: loaded, RequirePeriodCommit: true}
	row, err := pipeline.rowFor(bar, req, "binance-spot", 1)
	require.NoError(t, err)
	expectation, rows, enabled, err := periodCommitForDataset(req, "bars", []*storagepb.RowFieldUpsert{row})
	require.NoError(t, err)
	require.True(t, enabled)
	require.Equal(t, frequency, expectation.GetFrequency())
	require.Equal(t, frequency, rows[0].GetRow().GetKey().GetTimeSeries().GetFreq())
}

func TestPeriodFailureReporterReportsPermanentRetryExactlyOnce(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 11, 59, 0, 0, time.UTC)
	item := domain.CollectionItem{
		InstanceID: "instance-eth", SubjectID: "ETH-USDT", Symbol: "ETHUSDT", TargetDataTime: period.Format(time.RFC3339Nano),
		Provider: "binance", SourceID: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: "1m",
		SeriesIndex: 1, SeriesHash: "series-hash", ExpectedCount: 2,
	}
	taskRaw, err := json.Marshal(item)
	require.NoError(t, err)
	targetRaw, err := json.Marshal([]domain.WriteTarget{{
		ID: "target-eth", SpaceID: "crypto", InstanceID: item.InstanceID, TaskID: "bars-task", DatasetID: "bars",
		SeriesIndex: 1, SeriesHash: "series-hash", ExpectedCount: 2,
	}})
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "retry-eth", SourceBatchID: "sync-1", BatchKind: domain.BatchKindRealtime,
		InstanceID: item.InstanceID, RetryScope: "fetch", SubjectID: item.SubjectID, Frequency: "1m", TargetDataTime: period,
		TaskJSON: string(taskRaw), FailureTargetsJSON: string(targetRaw), Attempt: 3, Status: "permanent_failed",
		LastErrorType: "http_5xx", LastErrorSummary: "provider unavailable",
	}))

	storage := &recordingPeriodFailureStorage{}
	reporter := NewPeriodFailureReporter(db.FetchRetries(), func(string, string, string) (Storage, error) { return storage, nil }, "storage.local:11003", "crypto")
	require.NoError(t, reporter.RunOnce(ctx, "crypto"))
	require.Equal(t, 1, storage.calls)
	require.Len(t, storage.expectations, 1)
	exp := storage.expectations[0]
	require.Equal(t, "crypto", exp.GetSpaceId())
	require.Equal(t, "bars", exp.GetDatasetId())
	require.Equal(t, "1m", exp.GetFrequency())
	require.Equal(t, period.Unix(), exp.GetPeriodTime())
	require.Equal(t, "series-hash", exp.GetSeriesHash())
	require.Equal(t, uint32(2), exp.GetExpectedCount())
	require.Empty(t, exp.GetSeriesSnapshot(), "failure reporting uses the lightweight immutable identity")
	require.Equal(t, []uint32{1}, storage.indexes[0])

	stored, err := db.FetchRetries().Get(ctx, "crypto", "retry-eth")
	require.NoError(t, err)
	require.Equal(t, domain.PeriodFailureReportAcknowledged, stored.PeriodFailureReportState)
	require.Equal(t, "permanent_failed", stored.Status)

	require.NoError(t, reporter.RunOnce(ctx, "crypto"))
	require.Equal(t, 1, storage.calls, "reported terminal failure must not be sent again")
}

func TestSchedulerRetryFailureKeepsFrequencyIdentity(t *testing.T) {
	for _, frequency := range []string{"1M", "1H"} {
		t.Run(frequency, func(t *testing.T) {
			assertSchedulerRetryFailureKeepsFrequencyIdentity(t, frequency)
		})
	}
}

func assertSchedulerRetryFailureKeepsFrequencyIdentity(t *testing.T, frequency string) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	item := domain.CollectionItem{InstanceID: "instance-btc", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", TargetDataTime: period.Format(time.RFC3339Nano), Provider: "binance", SourceID: "binance", MarketType: "spot", DataType: "kline", DatasetID: "bars", Frequency: frequency, SeriesIndex: 0, SeriesHash: "period-hash", ExpectedCount: 1}
	taskRaw, err := json.Marshal(item)
	require.NoError(t, err)
	targetRaw, err := json.Marshal([]domain.WriteTarget{{ID: "target-btc", SpaceID: "crypto", InstanceID: item.InstanceID, TaskID: "period-task", DatasetID: "bars", SeriesIndex: 0, SeriesHash: item.SeriesHash, ExpectedCount: item.ExpectedCount}})
	require.NoError(t, err)
	require.NoError(t, db.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: "retry-btc-period", SourceBatchID: "sync-period", BatchKind: domain.BatchKindRealtime, InstanceID: item.InstanceID, RetryScope: "fetch", SubjectID: item.SubjectID, Frequency: frequency, TargetDataTime: period, TaskJSON: string(taskRaw), FailureTargetsJSON: string(targetRaw), Attempt: 3, Status: "permanent_failed", LastErrorType: "http_5xx", LastErrorSummary: "provider unavailable"}))

	storage := &recordingPeriodFailureStorage{}
	reporter := NewPeriodFailureReporter(db.FetchRetries(), func(string, string, string) (Storage, error) { return storage, nil }, "storage.local:11003", "crypto")
	require.NoError(t, reporter.RunOnce(ctx, "crypto"))
	require.Len(t, storage.expectations, 1)
	wantFrequency := frequency
	if frequency == "1H" {
		wantFrequency = "1h"
	}
	require.Equal(t, wantFrequency, storage.expectations[0].GetFrequency())
}
