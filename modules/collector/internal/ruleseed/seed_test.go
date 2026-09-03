package ruleseed

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validSeed = `rules:
  - space_id: crypto
    rule_id: builtin-binance-spot-kline-1m
    data_type: kline
    provider: binance
    market_type: spot
    enabled: true
    creator: moox-setup
    collect_params:
      provider: binance
      market_type: spot
      symbol_source: dataset
      symbol_dataset_id: dataset_binance_spot_symbols
      target_dataset_id: dataset_binance_spot_kline_1m
      frequency: 1m
`

func TestLoadRuleSeed(t *testing.T) {
	rules, err := loadRuleSeed(strings.NewReader(validSeed))
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, domain.TaskRule{
		SpaceID:       "crypto",
		RuleID:        "builtin-binance-spot-kline-1m",
		DataType:      "kline",
		Provider:      "binance",
		MarketType:    "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"dataset_binance_spot_symbols","target_dataset_id":"dataset_binance_spot_kline_1m","frequency":"1m","history_policy":{"mode":"live_only","batch_bar_limit":1000,"max_concurrency":1,"gap_repair_lookback":"0m","rate_budget_ratio":1}}`,
		Enabled:       true,
		Creator:       "moox-setup",
	}, rules[0])
}

func TestLoadRuleSeedRejectsUnknownField(t *testing.T) {
	_, err := loadRuleSeed(strings.NewReader("rules:\n  - rule_id: r1\n    mystery: true\n"))
	require.ErrorContains(t, err, "field mystery not found")
}

func TestLoadRuleSeedRejectsInvalidContracts(t *testing.T) {
	tests := map[string]string{
		"duplicate":       strings.Replace(validSeed, "rules:\n", "rules:\n"+strings.TrimPrefix(validSeed, "rules:\n"), 1),
		"legacy exchange": strings.Replace(validSeed, "provider: binance\n", "exchange: binance\n", 1),
		"mismatch":        strings.Replace(validSeed, "market_type: spot\n      symbol_source", "market_type: swap\n      symbol_source", 1),
		"bad frequency":   strings.Replace(validSeed, "frequency: 1m", "frequency: instant", 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadRuleSeed(strings.NewReader(raw))
			require.Error(t, err)
		})
	}
}

func TestSeedMissingIsIdempotentAndPreservesEdits(t *testing.T) {
	mgr, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	rules, err := loadRuleSeed(strings.NewReader(validSeed))
	require.NoError(t, err)
	ctx := context.Background()
	first, err := SeedMissing(ctx, mgr.TaskRules(), rules)
	require.NoError(t, err)
	assert.Equal(t, SeedSummary{Created: 1}, first)
	second, err := SeedMissing(ctx, mgr.TaskRules(), rules)
	require.NoError(t, err)
	assert.Equal(t, SeedSummary{Unchanged: 1}, second)
	require.NoError(t, mgr.TaskRules().SetEnabled(ctx, "crypto", rules[0].RuleID, false))
	third, err := SeedMissing(ctx, mgr.TaskRules(), rules)
	require.NoError(t, err)
	assert.Equal(t, SeedSummary{Unchanged: 1}, third)
	got, err := mgr.TaskRules().GetByRuleID(ctx, "crypto", rules[0].RuleID)
	require.NoError(t, err)
	assert.False(t, got.Enabled)
}
