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

func TestFilterInvokeRulesDropsRealtimeKlineRules(t *testing.T) {
	rules := filterInvokeRules([]domain.TaskRule{
		{RuleID: "symbols", DataType: "symbol"},
		{RuleID: "kline", DataType: "kline"},
	})
	assert.Equal(t, []string{"symbols"}, ruleIDs(rules))
}

func TestFilterMarketFetchRulesDropsLocalResampleRules(t *testing.T) {
	rules := filterMarketFetchRules([]domain.TaskRule{
		{RuleID: "symbols", DataType: "symbol"},
		{RuleID: "resample", DataType: "kline_resample"},
	})
	assert.Equal(t, []string{"symbols"}, ruleIDs(rules))
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

func TestGapAuditThresholdUsesWeekAndMonthIntervals(t *testing.T) {
	assert.Equal(t, 21*24*time.Hour, gapAuditThreshold("1w"))
	assert.Equal(t, 90*24*time.Hour, gapAuditThreshold("1M"))
}

func TestRealtimeBatchSizeFansOutAcrossCurrentFleet(t *testing.T) {
	nodes := make([]scfinvoker.Node, 10)
	for index := range nodes {
		nodes[index].Metadata = map[string]any{"realtime_batch_size": float64(64)}
	}
	scheduler := &Scheduler{BatchSize: MaxRealtimeItems}
	assert.Equal(t, 30, scheduler.realtimeBatchSize("binance", 479, nodes))

	nodes[0].Metadata["realtime_batch_size"] = float64(10)
	assert.Equal(t, 10, scheduler.realtimeBatchSize("binance", 479, nodes))
}

func TestInvocationCandidatesUsesOneDeterministicFailover(t *testing.T) {
	nodes := []scfinvoker.Node{{NodeID: "node-a"}, {NodeID: "node-b"}, {NodeID: "node-c"}}
	got := invocationCandidates(nodes[1], nodes)
	if assert.Len(t, got, 2) {
		assert.Equal(t, []string{"node-b", "node-c"}, []string{got[0].NodeID, got[1].NodeID})
	}
	assert.Equal(t, []string{"node-a"}, []string{invocationCandidates(nodes[0], nodes[:1])[0].NodeID})
}

func TestMarketFetchEventUsesGenericMarketDataContract(t *testing.T) {
	event, err := marketFetchEvent(Request{
		BatchID: "batch-1", ScheduleID: "schedule-1", BatchKind: domain.BatchKindRealtime, SpaceID: "crypto", DatasetID: "binance_spot_kline_1m",
		Frequency: "1m", Provider: "binance", MarketType: "spot",
		Items: []domain.CollectionItem{{TaskID: "task-btc", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", DataType: "kline", BarLimit: 3}},
	}, "ip://storage:11003")
	require.NoError(t, err)
	data, ok := event["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "crypto", data["market_id"])
	assert.Equal(t, "spot", data["instrument_type"])
	assert.Equal(t, "binance", data["provider_id"])
	assert.Equal(t, "spot_http", data["source_id"])
	assert.Equal(t, "venue:binance", data["series_tag"])
	assert.Equal(t, "kline", data["data_type"])
	assert.Equal(t, "realtime", data["batch_kind"])
	assert.Equal(t, "schedule-1", data["schedule_id"])
	assert.Equal(t, "batch-1", data["source_event_id"])
	assert.Equal(t, "batch-1", data["batch_id"])
	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "BTCUSDT", item["provider_symbol"])
	assert.Equal(t, "task-btc", item["task_id"])
}

func TestMarketFetchRetryEventPreservesBatchAndSourceEventIdentity(t *testing.T) {
	event, err := marketFetchEvent(Request{
		BatchID: "retry-batch-2", SourceEventID: "retry-key-1", ScheduleID: "retry:retry-key-1", BatchKind: domain.BatchKindRealtime,
		SpaceID: "crypto", DatasetID: "binance_spot_kline_1m", Frequency: "1m", Provider: "binance", MarketType: "spot",
		Items: []domain.CollectionItem{{TaskID: "task-btc", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", SourceEventID: "retry-key-1", DataType: "kline", BarLimit: 3}},
	}, "ip://storage:11003")
	require.NoError(t, err)
	data, ok := event["data"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "retry-batch-2", data["batch_id"])
	assert.Equal(t, "retry-key-1", data["source_event_id"])
	items, ok := data["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "retry-key-1", item["source_event_id"])
}

func TestPrepareRequestSourceEventsFreezesInitialPayloadIdentity(t *testing.T) {
	req := Request{BatchID: "batch-1", Items: []domain.CollectionItem{{SubjectID: "BTC-USDT", TargetDataTime: "2026-09-01T10:00:00Z"}}}
	prepareRequestSourceEvents(&req)
	want := retryKey("batch-1", "BTC-USDT", "2026-09-01T10:00:00Z")
	if req.Items[0].SourceEventID != want {
		t.Fatalf("source event id=%q, want %q", req.Items[0].SourceEventID, want)
	}
	prepareRequestSourceEvents(&req)
	if req.Items[0].SourceEventID != want {
		t.Fatalf("source event id changed on replay: %q, want %q", req.Items[0].SourceEventID, want)
	}
}

func TestSourceIDForIndexProviders(t *testing.T) {
	for _, test := range []struct {
		provider string
		want     string
	}{
		{provider: "cni", want: "index_cni_http"},
		{provider: "sw", want: "index_sw_http"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			assert.Equal(t, test.want, sourceIDFor(test.provider, "stock_cn", "index", ""))
		})
	}
}

func TestExpandRuleUsesOneCompleteExchangeSymbolSnapshot(t *testing.T) {
	scheduler := &Scheduler{}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "binance_spot_symbols", DataType: "symbol", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"exchange","target_dataset_id":"binance_spot_symbols","frequency":"1h"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1H"}, frequencies)
	if assert.Len(t, items, fullSymbolSnapshotShards) {
		assert.Equal(t, "binance_spot_symbols", items[0].DatasetID)
		assert.Equal(t, 0, items[0].SnapshotShardIndex)
		assert.Equal(t, fullSymbolSnapshotShards, items[0].SnapshotShardCount)
		assert.Equal(t, fullSymbolSnapshotShards-1, items[len(items)-1].SnapshotShardIndex)
	}
}

func TestExpandRuleCanonicalizesDefaultBinanceSymbolFrequency(t *testing.T) {
	scheduler := &Scheduler{}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto", RuleID: "binance_spot_symbols", DataType: "symbol", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"exchange","target_dataset_id":"binance_spot_symbols"}`,
	})
	assert.NoError(t, err)
	assert.Len(t, items, fullSymbolSnapshotShards)
	assert.Equal(t, []string{"1H"}, frequencies)
}

func TestRealtimeBatchSizeKeepsTDXInvocationSingleSymbol(t *testing.T) {
	scheduler := &Scheduler{}
	nodes := []scfinvoker.Node{{NodeID: "node-1"}, {NodeID: "node-2"}}
	assert.Equal(t, 1, scheduler.realtimeBatchSize("tdx", 479, nodes))
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
