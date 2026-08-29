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

func TestBuildGapAuditPlanUsesCoverageStartForMissingWatermark(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	coverageStart := time.Date(2026, time.August, 27, 8, 30, 0, 0, time.UTC)

	plan, ok := buildGapAuditPlan(
		now,
		domain.TaskRule{CoverageStartTime: &coverageStart},
		domain.TaskInstance{Frequency: "1m"},
		time.Time{},
		false,
	)
	require.True(t, ok)
	assert.Equal(t, domain.BatchKindBackfill, plan.Kind)
	assert.Equal(t, coverageStart, plan.Start)
	assert.Equal(t, 1000, plan.BarLimit)
}

func TestBuildGapAuditPlanUsesGapRepairForStaleWatermark(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	coverageStart := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	watermark := now.Add(-2 * time.Hour)

	plan, ok := buildGapAuditPlan(
		now,
		domain.TaskRule{CoverageStartTime: &coverageStart},
		domain.TaskInstance{Frequency: "1m"},
		watermark,
		true,
	)
	require.True(t, ok)
	assert.Equal(t, domain.BatchKindGapRepair, plan.Kind)
	assert.Equal(t, watermark, plan.Start)
}

func TestBuildGapAuditPlanFailsClosedWithoutCoverageStart(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	plan, ok := buildGapAuditPlan(
		now,
		domain.TaskRule{CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m","history_policy":{"mode":"live_only","gap_repair_lookback":"30m"}}`},
		domain.TaskInstance{Frequency: "1m"},
		now.Add(-2*time.Hour),
		true,
	)
	assert.False(t, ok)
	assert.Zero(t, plan.Start)
}

func TestBuildGapAuditPlanNeverCrossesCoverageOrRepairFloor(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	coverageStart := now.Add(-90 * time.Minute)
	watermark := now.Add(-3 * time.Hour)
	rule := domain.TaskRule{
		Provider: "binance", MarketType: "spot", DataType: "kline", CoverageStartTime: &coverageStart,
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"bars","frequency":"1m","history_policy":{"mode":"live_only","batch_bar_limit":30,"max_concurrency":1,"gap_repair_lookback":"20m","rate_budget_ratio":1}}`,
	}

	plan, ok := buildGapAuditPlan(now, rule, domain.TaskInstance{SpaceID: "crypto_market", Provider: "binance", Frequency: "1m"}, watermark, true)
	require.True(t, ok)
	assert.Equal(t, now.Add(-20*time.Minute), plan.Start)
}

func TestBuildGapAuditPlanFailsClosedForStockHistory(t *testing.T) {
	now := time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)
	coverageStart := now.Add(-24 * time.Hour)
	rule := domain.TaskRule{
		Provider: "stock_cn_multi", MarketType: "equity", DataType: "kline", CoverageStartTime: &coverageStart,
		CollectParams: `{"provider":"stock_cn_multi","market_type":"equity","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"stock_cn_kline","frequency":"1m","history_policy":{"mode":"live_only","batch_bar_limit":1000,"max_concurrency":1,"gap_repair_lookback":"0m","rate_budget_ratio":1}}`,
	}

	plan, ok := buildGapAuditPlan(now, rule, domain.TaskInstance{SpaceID: StockCNSpaceID, Provider: "stock_cn_multi", Frequency: "1m"}, time.Time{}, false)
	assert.False(t, ok)
	assert.Zero(t, plan.Start)
}

func TestGapAuditSeriesTagMatchesStockRowKey(t *testing.T) {
	assert.Equal(t, "", gapAuditSeriesTag(domain.TaskInstance{SpaceID: StockCNSpaceID, Provider: "stock_cn_multi"}))
	assert.Equal(t, "venue:binance", gapAuditSeriesTag(domain.TaskInstance{SpaceID: "crypto_market", Provider: "binance"}))
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

func TestExpandRuleUsesAllShardsForExchangeSymbolSnapshot(t *testing.T) {
	scheduler := &Scheduler{}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto_market", RuleID: "binance_spot_symbols", DataType: "symbol", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"exchange","target_dataset_id":"binance_spot_symbols","frequency":"1h"}`,
	})
	assert.NoError(t, err)
	assert.Equal(t, []string{"1h"}, frequencies)
	if assert.Len(t, items, fullSymbolSnapshotShards) {
		assert.Equal(t, "binance_spot_symbols", items[0].DatasetID)
		assert.Equal(t, 0, items[0].SnapshotShardIndex)
		assert.Equal(t, fullSymbolSnapshotShards, items[0].SnapshotShardCount)
		assert.Equal(t, fullSymbolSnapshotShards-1, items[len(items)-1].SnapshotShardIndex)
	}
}

func TestExpandRuleUsesExplicitExternalSymbolForKline(t *testing.T) {
	scheduler := &Scheduler{
		SpaceID: "crypto_market",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}},
	}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto_market", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
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
		SpaceID: "crypto_market",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{{SubjectID: "币安人生-USDT", ExternalSymbol: "BINANCELIFEUSDT", Status: "active"}}},
	}
	items, frequencies, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto_market", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
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
		SpaceID: "crypto_market",
		Symbols: datasetSourceStub{subjects: []domain.DatasetSubject{
			{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"},
			{SubjectID: "币安人生-USDT", ExternalSymbol: "", Status: "active"},
		}},
	}
	items, _, err := scheduler.expandRule(t.Context(), domain.TaskRule{
		SpaceID: "crypto_market", RuleID: "bars", DataType: "kline", Provider: "binance", MarketType: "spot",
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
