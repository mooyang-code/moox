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
				gotDatasets = append(gotDatasets, strings.TrimSpace(fmt.Sprint(target["DatasetID"])))
			}
			require.ElementsMatch(t, tc.wantDatasets, gotDatasets)
		})
	}
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
	require.NoError(t, scheduler.recoverDue(ctx, "crypto", []scfinvoker.Node{{NodeID: "node"}}, now))
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
	firstSnapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1h", PeriodTime: firstPeriod})
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
	nextSnapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1h", PeriodTime: nextPeriod})
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(1), nextSnapshot.ExpectedCount)
	require.Equal(t, []string{"BTC-USDT"}, periodSeriesSubjectsForScheduler(nextSnapshot))
	require.Equal(t, 2, source.resolveSubjectsCalls, "the next period must use the refreshed Storage Tag members")
}

func TestSchedulerPeriodSeriesSnapshotCleanupRunsAgainAfterItsInterval(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 30, 9, 10, 0, 0, time.UTC)
	makeSnapshot := func(period time.Time) domain.PeriodSeriesSnapshot {
		tag := "venue:binance|market:spot|source:spot_http"
		key := domain.CanonicalSeriesKey("binance", "spot_http", "spot", "BTC-USDT", tag)
		hash := domain.SeriesSetHash([]string{key})
		return domain.PeriodSeriesSnapshot{
			Key:        domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period},
			SeriesHash: hash, ExpectedCount: 1, Entries: []domain.PeriodSeriesSnapshotEntry{{
				SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period,
				SeriesIndex: 0, SeriesKey: key, SubjectID: "BTC-USDT", Provider: "binance", SourceID: "spot_http",
				MarketType: "spot", ProviderSymbol: "BTCUSDT", SeriesTag: tag, SeriesHash: hash, ExpectedCount: 1,
			}}}
	}
	oldPeriod := now.Add(-31 * 24 * time.Hour)
	_, _, err := db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, makeSnapshot(oldPeriod))
	require.NoError(t, err)
	scheduler := &Scheduler{PeriodSeriesSnapshot: db.PeriodSeriesSnapshot(), SpaceID: "crypto"}
	scheduler.cleanupExpiredPeriodSeriesSnapshots(ctx, now)
	snapshot, found, err := db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: oldPeriod})
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, snapshot.Entries)

	secondOldPeriod := now.Add(-32 * 24 * time.Hour)
	_, _, err = db.PeriodSeriesSnapshot().CreatePeriodSeriesSnapshotIfAbsent(ctx, makeSnapshot(secondOldPeriod))
	require.NoError(t, err)
	scheduler.cleanupExpiredPeriodSeriesSnapshots(ctx, now.Add(30*time.Second))
	snapshot, found, err = db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: secondOldPeriod})
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, snapshot.Entries, 1, "cleanup stays throttled between bounded hourly passes")
	scheduler.cleanupExpiredPeriodSeriesSnapshots(ctx, now.Add(time.Minute))
	snapshot, found, err = db.PeriodSeriesSnapshot().GetPeriodSeriesSnapshot(ctx, domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: secondOldPeriod})
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, snapshot.Entries, "period snapshots are reclaimed by subsequent scheduler ticks")
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
}

func (s *recordingPeriodFailureStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}
func (s *recordingPeriodFailureStorage) EnsureDatasetPeriod(context.Context, *storagepb.DatasetPeriodExpectation) error {
	return nil
}
func (s *recordingPeriodFailureStorage) CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error {
	return nil
}
func (s *recordingPeriodFailureStorage) RecordDatasetPeriodFailures(_ context.Context, exp *storagepb.DatasetPeriodExpectation, indexes []uint32) error {
	s.calls++
	s.expectations = append(s.expectations, exp)
	s.indexes = append(s.indexes, append([]uint32(nil), indexes...))
	return nil
}

func TestSchedulerReportsPermanentRetryPeriodFailureExactlyOnce(t *testing.T) {
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
	scheduler := &Scheduler{
		Retries: db.FetchRetries(), StorageTarget: "storage.local:11003",
		Storage: func(string, string, string) (Storage, error) { return storage, nil },
	}
	require.NoError(t, scheduler.reportPendingPeriodFailures(ctx, "crypto"))
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
	require.True(t, stored.PeriodFailureReported)
	require.Equal(t, "permanent_failed", stored.Status)

	require.NoError(t, scheduler.reportPendingPeriodFailures(ctx, "crypto"))
	require.Equal(t, 1, storage.calls, "reported terminal failure must not be sent again")
}
