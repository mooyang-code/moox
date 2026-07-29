package observability

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

type bindingSourceStub struct {
	rows []domain.FactorBinding
	err  error
}

func (s *bindingSourceStub) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return s.rows, s.err
}

type factorRegistryStub struct {
	items  []report.DatasetExpectation
	errors int
}

func (s *factorRegistryStub) ReplaceExpected(items []report.DatasetExpectation) error {
	s.items = append([]report.DatasetExpectation(nil), items...)
	return nil
}
func (s *factorRegistryStub) ObserveInventoryRefreshError() { s.errors++ }

func TestRealtimeInventoryUsesExecutableBindingTargets(t *testing.T) {
	source := &bindingSourceStub{rows: []domain.FactorBinding{
		{BindingID: "a", SpaceID: "crypto", TargetDataset: "bars_factor", Freq: "1m"},
		{BindingID: "duplicate", SpaceID: "crypto", TargetDataset: "bars_factor", Freq: "1m"},
		{BindingID: "b", SpaceID: "crypto", TargetDataset: "bars_factor", Freq: "5m"},
	}}
	registry := &factorRegistryStub{}
	inventory := NewRealtimeInventory(source, registry)

	require.NoError(t, inventory.Refresh(context.Background()))
	require.Equal(t, []report.DatasetExpectation{
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars_factor", Freq: "1m"}, Interval: time.Minute},
		{Key: report.DatasetKey{SpaceID: "crypto", DatasetID: "bars_factor", Freq: "5m"}, Interval: 5 * time.Minute},
	}, registry.items)
}

func TestRealtimeInventoryInvalidFreqRetainsPreviousSnapshot(t *testing.T) {
	source := &bindingSourceStub{rows: []domain.FactorBinding{
		{BindingID: "valid", SpaceID: "crypto", TargetDataset: "bars_factor", Freq: "1m"},
	}}
	registry := &factorRegistryStub{}
	inventory := NewRealtimeInventory(source, registry)
	require.NoError(t, inventory.Refresh(context.Background()))
	previous := append([]report.DatasetExpectation(nil), registry.items...)

	source.rows = []domain.FactorBinding{{BindingID: "invalid", SpaceID: "crypto", TargetDataset: "bars_factor", Freq: "0s"}}
	inventory.MarkDirty()
	require.Error(t, inventory.Refresh(context.Background()))
	require.Equal(t, previous, registry.items)
	require.Equal(t, 1, registry.errors)
	require.True(t, inventory.Due(time.Now()))
}

func TestParseFrequencyMatchesStorageFrequencyContract(t *testing.T) {
	tests := map[string]time.Duration{
		"1m": time.Minute,
		"1H": time.Hour,
		"1d": 24 * time.Hour,
		"1w": 7 * 24 * time.Hour,
		"1M": 30 * 24 * time.Hour,
		"1y": 365 * 24 * time.Hour,
	}
	for freq, expected := range tests {
		t.Run(freq, func(t *testing.T) {
			actual, err := parseFrequency(freq)
			require.NoError(t, err)
			require.Equal(t, expected, actual)
		})
	}
}
