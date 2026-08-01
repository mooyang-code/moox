package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

type ruleSourceStub struct {
	rules []domain.TaskRule
	err   error
}

func (s *ruleSourceStub) ListEnabledAll(context.Context, int) ([]domain.TaskRule, error) {
	return s.rules, s.err
}

type registryStub struct {
	items  []report.DatasetExpectation
	errors int
}

func (s *registryStub) ReplaceExpected(items []report.DatasetExpectation) error {
	s.items = append([]report.DatasetExpectation(nil), items...)
	return nil
}
func (s *registryStub) ObserveInventoryRefreshError() { s.errors++ }

func collectorRule(id string, enabled bool, dataType, target, frequency string) domain.TaskRule {
	params := `{"provider":"binance","market_type":"spot","symbol_source":"manual","symbols":["BTC-USDT"],"target_dataset_id":"` + target + `","frequency":"` + frequency + `"}`
	if dataType == "kline" {
		params = `{"provider":"binance","market_type":"spot","symbol_source":"dataset","symbol_dataset_id":"symbols","target_dataset_id":"` + target + `","frequency":"` + frequency + `"}`
	}
	return domain.TaskRule{SpaceID: "crypto", RuleID: id, DataType: dataType, Provider: "binance", MarketType: "spot", CollectParams: params, Enabled: enabled}
}

func TestRealtimeInventorySelectsEnabledScheduledKlineAndDeduplicates(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.TaskRule{
		collectorRule("live", true, "kline", "bars", "1m"),
		collectorRule("live-5m", true, "kline", "bars", "5m"),
		collectorRule("duplicate", true, "kline", "bars", "1m"),
		collectorRule("batch", true, "kline", "batch-bars", "1m"),
		collectorRule("symbol", true, "symbol", "symbols", "1m"),
		collectorRule("disabled", false, "kline", "disabled-bars", "1m"),
	}}
	registry := &registryStub{}
	inventory := NewRealtimeInventory(source, registry)

	require.NoError(t, inventory.Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1m"}, Interval: time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "5m"}, Interval: 5 * time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "batch-bars", Freq: "1m"}, Interval: time.Minute},
	}, registry.items)
	require.False(t, inventory.Due(time.Now()))
}

func TestRealtimeInventoryCanonicalizesFrequencyAliasesBeforeDeduplication(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.TaskRule{
		collectorRule("lowercase", true, "kline", "bars", "1h"),
		collectorRule("canonical", true, "kline", "bars", "1H"),
	}}
	registry := &registryStub{}

	require.NoError(t, NewRealtimeInventory(source, registry).Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{{
		Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1H"}, Interval: time.Hour,
	}}, registry.items)
}

func TestRealtimeInventoryFailureRetainsPreviousSnapshot(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.TaskRule{collectorRule("live", true, "kline", "bars", "1m")}}
	registry := &registryStub{}
	inventory := NewRealtimeInventory(source, registry)
	require.NoError(t, inventory.Refresh(context.Background()))
	previous := append([]report.DatasetExpectation(nil), registry.items...)

	source.rules = []domain.TaskRule{{SpaceID: "crypto", RuleID: "invalid", Enabled: true, CollectParams: "{"}}
	inventory.MarkDirty()
	require.Error(t, inventory.Refresh(context.Background()))
	require.Equal(t, previous, registry.items)
	require.Equal(t, 1, registry.errors)
	require.True(t, inventory.Due(time.Now()))

	source.err = errors.New("db unavailable")
	require.Error(t, inventory.Refresh(context.Background()))
	require.Equal(t, previous, registry.items)
}
