package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
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
		gotDatasets = append(gotDatasets, strings.TrimSpace(fmt.Sprint(target["DatasetID"])))
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
				gotDatasets = append(gotDatasets, strings.TrimSpace(fmt.Sprint(target["DatasetID"])))
			}
			require.ElementsMatch(t, tc.wantDatasets, gotDatasets)
		})
	}
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
}

func (r *recordingMarketFetchInvoker) ListMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
	return append([]scfinvoker.Node(nil), r.invokeNodes...), nil
}

func (r *recordingMarketFetchInvoker) ListTimerMarketFetchers(context.Context, string) ([]scfinvoker.Node, error) {
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
