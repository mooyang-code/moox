package pipeline

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

func TestBuildLivePlanUsesEventUniverse(t *testing.T) {
	set := domain.FactorTask{
		SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.TaskStatusEnabled,
	}
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set: set,
		Members: domain.AsMembers(
			domain.FactorDef{FactorID: "short", LookbackPeriods: 5},
			domain.FactorDef{FactorID: "long", LookbackPeriods: 20},
		),
		PeriodTime: target, Universe: []string{"SOL", "ETH", "BTC", "BTC"},
		UpstreamFailed: []string{"ETH", "SOL"}, CarryColumns: []string{"volume", "close"}, TriggerEventID: "evt-1",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, plan.Expected)
	require.Equal(t, []string{"BTC"}, plan.Available)
	require.Equal(t, []string{"ETH", "SOL"}, plan.UpstreamFailed)
	require.Equal(t, target.Add(-19*time.Minute), plan.TargetStart)
	require.Equal(t, target, plan.PeriodTime)
	require.Equal(t, target.Add(time.Minute), plan.TargetEnd)
	require.Equal(t, []string{"close", "volume"}, plan.CarryColumns)
	require.Equal(t, []string{"long", "short"}, []string{plan.Members[0].Key, plan.Members[1].Key})
	require.True(t, plan.WriteCarry)
}

func TestBuildLivePlanRejectsUnalignedPeriod(t *testing.T) {
	_, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set:        domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.TaskStatusEnabled},
		PeriodTime: time.Date(2026, 10, 4, 0, 10, 30, 0, time.UTC),
	})
	require.ErrorContains(t, err, "not aligned")
}
