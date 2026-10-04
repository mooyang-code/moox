package pipeline

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

func TestBuildLivePlanIntersectsUniverseAndScope(t *testing.T) {
	set := domain.FactorSet{
		SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled,
		SubjectMode: domain.SubjectModeInclude, Subjects: []string{"BTC", "ETH"},
	}
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set: set,
		Factors: []domain.FactorDef{
			{FactorID: "short", LookbackPeriods: 5, Status: domain.FactorStatusEnabled},
			{FactorID: "disabled", LookbackPeriods: 200, Status: domain.FactorStatusDisabled},
			{FactorID: "long", LookbackPeriods: 20, Status: domain.FactorStatusEnabled},
		},
		PeriodTime: target, Universe: []string{"SOL", "ETH", "BTC", "BTC"},
		UpstreamFailed: []string{"ETH", "SOL"}, CarryColumns: []string{"volume", "close"}, TriggerEventID: "evt-1",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH"}, plan.Expected)
	require.Equal(t, []string{"BTC"}, plan.Available)
	require.Equal(t, []string{"ETH"}, plan.UpstreamFailed)
	require.Equal(t, target.Add(-19*time.Minute), plan.TargetStart)
	require.Equal(t, target, plan.PeriodTime)
	require.Equal(t, target.Add(time.Minute), plan.TargetEnd)
	require.Equal(t, []string{"close", "volume"}, plan.CarryColumns)
	require.Equal(t, []string{"long", "short"}, []string{plan.Factors[0].FactorID, plan.Factors[1].FactorID})
	require.True(t, plan.WriteCarry)
}

func TestBuildLivePlanRejectsUnalignedPeriod(t *testing.T) {
	_, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set:        domain.FactorSet{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.SetStatusEnabled},
		PeriodTime: time.Date(2026, 10, 4, 0, 10, 30, 0, time.UTC),
	})
	require.ErrorContains(t, err, "not aligned")
}
