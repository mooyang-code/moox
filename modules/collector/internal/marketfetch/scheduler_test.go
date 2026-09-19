package marketfetch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotateRulesAfterAdvancesPastLastCappedRule(t *testing.T) {
	rules := []domain.CollectionTask{{TaskID: "rule-a"}, {TaskID: "rule-b"}, {TaskID: "rule-c"}}
	rotated := rotateRulesAfter(rules, "rule-a")

	assert.Equal(t, []string{"rule-b", "rule-c", "rule-a"}, ruleIDs(rotated))
	assert.Equal(t, []string{"rule-a", "rule-b", "rule-c"}, ruleIDs(rotateRulesAfter(rules, "missing")))
}

func TestNormalizedBatchIdentityUsesNormalizedItemMarketType(t *testing.T) {
	provider, marketType := normalizedBatchIdentity(
		domain.CollectionItem{Provider: "binance", MarketType: "spot"},
		domain.CollectionTask{Provider: "binance", MarketType: ""},
	)

	assert.Equal(t, "binance", provider)
	assert.Equal(t, "spot", marketType)
}

func TestCollectionItemTaskIDSeparatesInstrumentSnapshotShards(t *testing.T) {
	seen := make(map[string]struct{}, 32)
	for shard := 0; shard < 32; shard++ {
		id := collectionItemTaskID("stockcn", "stockcn-symbols", domain.CollectionItem{
			SubjectID: "stockcn", DataType: "instrument", DatasetID: "dataset_stockcn_instruments",
			SnapshotShardIndex: shard, SnapshotShardCount: 32,
		}, "")
		require.NotEmpty(t, id)
		_, duplicate := seen[id]
		require.False(t, duplicate, "snapshot shard %d reused task id %q", shard, id)
		seen[id] = struct{}{}
	}

	item := domain.CollectionItem{SubjectID: "BTC-USDT", DataType: "kline", DatasetID: "bars", Provider: "binance", MarketType: "spot"}
	assert.Equal(t, collectionItemTaskID("crypto", "kline", item, "1m"), collectionItemTaskID("crypto", "kline", item, "1m"))
}

func TestFilterInvokeRulesDropsRealtimeKlineRules(t *testing.T) {
	rules := filterInvokeRules([]domain.CollectionTask{
		{TaskID: "instruments", DataType: "instrument"},
		{TaskID: "kline", DataType: "kline"},
	})
	assert.Equal(t, []string{"instruments"}, ruleIDs(rules))
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

func TestFilterMarketFetchRulesDropsLocalResampleRules(t *testing.T) {
	rules := filterMarketFetchRules([]domain.CollectionTask{
		{TaskID: "instruments", DataType: "instrument"},
		{TaskID: "resample", DataType: "kline_resample"},
	})
	assert.Equal(t, []string{"instruments"}, ruleIDs(rules))
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

func TestExpandRuleUsesOneShardForCryptoExchangeInstrumentSnapshot(t *testing.T) {
	scheduler := &Scheduler{}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_instruments", DataType: "instrument", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"exchange","target_dataset_id":"dataset_binance_spot_symbols","frequency":"1h"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1h"}, frequencies)
	if assert.Len(t, items, 1) {
		assert.Equal(t, "dataset_binance_spot_symbols", items[0].DatasetID)
		assert.Equal(t, 0, items[0].SnapshotShardIndex)
		assert.Equal(t, 1, items[0].SnapshotShardCount)
	}
}

func TestExpandRuleKeepsStockCNInstrumentShards(t *testing.T) {
	scheduler := &Scheduler{}
	items, _, err := scheduler.expandRule(t.Context(), domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "stockcn_instruments", DataType: "instrument", Provider: "sina", MarketType: "equity",
		CollectParams: `{"provider":"sina","market_type":"equity","symbol_source":"exchange","target_dataset_id":"dataset_stockcn_instruments","frequency":"1h"}`,
	})
	assert.NoError(t, err)
	assert.Len(t, items, fullInstrumentSnapshotShards)
	assert.Equal(t, fullInstrumentSnapshotShards, items[0].SnapshotShardCount)
}

func TestBatchKindForRuleUsesPublicInstrumentDataType(t *testing.T) {
	assert.Equal(t, domain.BatchKindInstrumentSnapshot, batchKindForRule(domain.CollectionTask{DataType: domain.InstrumentDataType}))
	assert.Equal(t, domain.BatchKindRealtime, batchKindForRule(domain.CollectionTask{DataType: "kline"}))
}

func TestBatchCompletionDeadlineAllowsInstrumentSnapshotProvidersToFinish(t *testing.T) {
	assert.Equal(t, 6*time.Minute, batchCompletionDeadline(domain.BatchKindInstrumentSnapshot))
	assert.Equal(t, 70*time.Second, batchCompletionDeadline(domain.BatchKindRealtime))
}

func TestExpandRuleUsesExplicitExternalSymbolForKline(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID: "crypto",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
	}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1m"}, frequencies)
	if assert.Len(t, items, 1) {
		assert.Equal(t, "BTCUSDT", items[0].Symbol)
	}
}

func TestExpandRuleAllowsUnicodeSubjectNames(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID: "crypto",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "币安人生-USDT", ExternalSymbol: "BINANCELIFEUSDT", Status: "active"}}},
	}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"1m"}, frequencies)
	require.Len(t, items, 1)
	if len(items) == 1 {
		assert.Equal(t, "币安人生-USDT", items[0].SubjectID)
		assert.Equal(t, "BINANCELIFEUSDT", items[0].Symbol)
	}
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

func TestTickInvokesPriorityCryptoMinuteWhenTimersOwnRealtime(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_kline", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
		Enabled:       true,
	}
	require.NoError(t, db.Tasks().Create(ctx, rule))
	invoker := &recordingMarketFetchInvoker{
		timerNodes: []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "fn-timer-1", Region: "ap-hongkong", TriggerType: "timer"}},
	}
	now := time.Date(2026, 9, 15, 3, 12, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Rules:                 db.Tasks(),
		Instances:             db.TaskInstances(),
		Batches:               db.FetchBatches(),
		Invoker:               invoker,
		InvokeNonRealtimeOnly: true,
		SpaceID:               "crypto",
		Now:                   func() time.Time { return now },
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "AAA-USDT", ExternalSymbol: "AAAUSDT", Status: "active"},
			{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
			{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT", Status: "active"},
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
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_kline", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
		Enabled:       true,
	}
	require.NoError(t, db.Tasks().Create(ctx, rule))
	invoker := &recordingMarketFetchInvoker{
		invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}},
		timerNodes:  []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "fn-timer-1", Region: "ap-hongkong", TriggerType: "timer"}},
	}
	now := time.Date(2026, 9, 15, 3, 12, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Rules:     db.Tasks(),
		Instances: db.TaskInstances(),
		Batches:   db.FetchBatches(),
		Invoker:   invoker,
		SpaceID:   "crypto",
		Now:       func() time.Time { return now },
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "AAA-USDT", ExternalSymbol: "AAAUSDT", Status: "active"},
			{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
			{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT", Status: "active"},
		}},
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) > 0 }, 2*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool {
		for _, invoke := range invoker.snapshot() {
			if invoke.nodeID == "invoke-1" {
				return true
			}
		}
		return false
	}, 200*time.Millisecond, 20*time.Millisecond)
	require.Equal(t, []string{"AAA-USDT", "BTC-USDT", "ETH-USDT"}, invokedSubjectIDs(invoker.snapshot()))
	for _, invoke := range invoker.snapshot() {
		require.Equal(t, "timer-1", invoke.nodeID)
	}
}

func TestTickPrefersInvokeNodesForPriorityCryptoMinute(t *testing.T) {
	db := newTestMarketFetchStore(t)
	ctx := context.Background()
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "binance_spot_kline", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m"}`,
		Enabled:       true,
	}
	require.NoError(t, db.Tasks().Create(ctx, rule))
	invoker := &recordingMarketFetchInvoker{
		invokeNodes: []scfinvoker.Node{{NodeID: "invoke-1", FunctionName: "fn-invoke-1", Region: "ap-hongkong", TriggerType: "invoke"}},
		timerNodes:  []scfinvoker.Node{{NodeID: "timer-1", FunctionName: "fn-timer-1", Region: "ap-hongkong", TriggerType: "timer"}},
	}
	now := time.Date(2026, 9, 15, 3, 12, 8, 0, time.UTC)
	scheduler := &Scheduler{
		Rules:                 db.Tasks(),
		Instances:             db.TaskInstances(),
		Batches:               db.FetchBatches(),
		Invoker:               invoker,
		InvokeNonRealtimeOnly: true,
		SpaceID:               "crypto",
		Now:                   func() time.Time { return now },
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
		}},
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return len(invoker.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, "invoke-1", invoker.snapshot()[0].nodeID)
	require.Equal(t, []string{"BTC-USDT"}, invokedSubjectIDs(invoker.snapshot()))
}

func TestExpandRuleSkipsMalformedSnapshotSubjects(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID: "crypto",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
			{SubjectID: "币安人生-USDT", ExternalSymbol: "", Status: "active"},
		}},
	}
	items, _, err := scheduler.expandRule(t.Context(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m"}`,
	})
	assert.NoError(t, err)
	if assert.Len(t, items, 1) {
		assert.Equal(t, "BTC-USDT", items[0].SubjectID)
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

func ruleIDs(rules []domain.CollectionTask) []string {
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.TaskID)
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
