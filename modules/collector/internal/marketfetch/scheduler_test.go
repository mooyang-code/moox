package marketfetch

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, 48, scheduler.realtimeBatchSize(479, nodes))

	nodes[0].Metadata["realtime_batch_size"] = float64(10)
	assert.Equal(t, 10, scheduler.realtimeBatchSize(479, nodes))
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

func ruleIDs(rules []domain.TaskRule) []string {
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.RuleID)
	}
	return ids
}
