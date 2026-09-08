package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotateRulesAfterAdvancesPastLastCappedRule(t *testing.T) {
	rules := []domain.TaskRule{{RuleID: "rule-a"}, {RuleID: "rule-b"}, {RuleID: "rule-c"}}
	rotated := rotateRulesAfter(rules, "rule-a")

	assert.Equal(t, []string{"rule-b", "rule-c", "rule-a"}, ruleIDs(rotated))
	assert.Equal(t, []string{"rule-a", "rule-b", "rule-c"}, ruleIDs(rotateRulesAfter(rules, "missing")))
}

func TestNormalizedBatchIdentityUsesNormalizedItemMarketType(t *testing.T) {
	provider, marketType := normalizedBatchIdentity(
		domain.CollectionItem{Provider: "binance", MarketType: "spot"},
		domain.TaskRule{Provider: "binance", MarketType: ""},
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
	rules := filterInvokeRules([]domain.TaskRule{
		{RuleID: "instruments", DataType: "instrument"},
		{RuleID: "kline", DataType: "kline"},
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
	rules := filterMarketFetchRules([]domain.TaskRule{
		{RuleID: "instruments", DataType: "instrument"},
		{RuleID: "resample", DataType: "kline_resample"},
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
	assert.Equal(t, 30, scheduler.realtimeBatchSize(479, nodes))

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
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "binance_spot_instruments", DataType: "instrument", Provider: "binance", MarketType: "spot",
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
	items, _, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "stockcn", RuleID: "stockcn_instruments", DataType: "instrument", Provider: "sina", MarketType: "equity",
		CollectParams: `{"provider":"sina","market_type":"equity","symbol_source":"exchange","target_dataset_id":"dataset_stockcn_instruments","frequency":"1h"}`,
	})
	assert.NoError(t, err)
	assert.Len(t, items, fullInstrumentSnapshotShards)
	assert.Equal(t, fullInstrumentSnapshotShards, items[0].SnapshotShardCount)
}

func TestBatchKindForRuleUsesPublicInstrumentDataType(t *testing.T) {
	assert.Equal(t, domain.BatchKindInstrumentSnapshot, batchKindForRule(domain.TaskRule{DataType: domain.InstrumentDataType}))
	assert.Equal(t, domain.BatchKindRealtime, batchKindForRule(domain.TaskRule{DataType: "kline"}))
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
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
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
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
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

func TestExpandRuleSkipsMalformedSnapshotSubjects(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID: "crypto",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
			{SubjectID: "币安人生-USDT", ExternalSymbol: "", Status: "active"},
		}},
	}
	items, _, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
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

func ruleIDs(rules []domain.TaskRule) []string {
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.RuleID)
	}
	return ids
}
