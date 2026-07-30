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

func collectorRule(id string, enabled, live bool, dataType, target, schedule string, frequencies ...string) domain.TaskRule {
	params := `{"source":{"kind":"none"},"collector":{"exchange":"binance","market":"spot","data_type":"symbol"},"target":{"dataset_id":"` + target + `"},"schedule":{"interval":"` + schedule + `"}}`
	if dataType == "kline" {
		params = `{"source":{"kind":"dataset_subjects","dataset_id":"symbols"},"collector":{"exchange":"binance","market":"spot","data_type":"kline","live":` +
			map[bool]string{true: "true", false: "false"}[live] + `,"intervals":[`
		for index, freq := range frequencies {
			if index > 0 {
				params += ","
			}
			params += `"` + freq + `"`
		}
		params += `]},"target":{"dataset_id":"` + target + `"},"schedule":{"interval":"` + schedule + `"}}`
	}
	return domain.TaskRule{SpaceID: "crypto", RuleID: id, DataType: dataType, Exchange: "binance", CollectParams: params, Enabled: enabled}
}

func TestRealtimeInventorySelectsEnabledScheduledKlineAndDeduplicates(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.TaskRule{
		collectorRule("live", true, true, "kline", "bars", "2m", "1m", "5m"),
		collectorRule("duplicate", true, true, "kline", "bars", "3m", "1m"),
		collectorRule("batch", true, false, "kline", "batch-bars", "1m", "1m"),
		collectorRule("symbol", true, false, "symbol", "symbols", "1m"),
		collectorRule("disabled", false, true, "kline", "disabled-bars", "1m", "1m"),
	}}
	registry := &registryStub{}
	inventory := NewRealtimeInventory(source, registry)

	require.NoError(t, inventory.Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1m"}, Interval: 2 * time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "5m"}, Interval: 2 * time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "batch-bars", Freq: "1m"}, Interval: time.Minute},
	}, registry.items)
	require.False(t, inventory.Due(time.Now()))
}

func TestRealtimeInventoryCanonicalizesFrequencyAliasesBeforeDeduplication(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.TaskRule{
		collectorRule("lowercase", true, false, "kline", "bars", "2h", "1h"),
		collectorRule("canonical", true, false, "kline", "bars", "3h", "1H"),
	}}
	registry := &registryStub{}

	require.NoError(t, NewRealtimeInventory(source, registry).Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{{
		Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars", Freq: "1H"}, Interval: 2 * time.Hour,
	}}, registry.items)
}

func TestRealtimeInventoryFailureRetainsPreviousSnapshot(t *testing.T) {
	source := &ruleSourceStub{rules: []domain.TaskRule{collectorRule("live", true, true, "kline", "bars", "1m", "1m")}}
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
