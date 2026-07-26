package scheduler

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBuildTaskUsesAllFactorsAndMaximumLookback(t *testing.T) {
	task, err := BuildTask(TaskScope{
		TaskID: "task-1", TriggerType: "recalc", SpaceID: "crypto",
		SourceDataset: "bars", TargetDataset: "bars_factor", SubjectID: "BTC",
		Freq: "1m", StartTime: time.Unix(1, 0), EndTime: time.Unix(3, 0),
	}, []domain.FactorDef{
		{FactorID: "bias", Name: "Bias", SourceHash: "h1", Periods: []int{20}, LookbackBars: 100, Depends: []string{"funding_rate"}, Status: domain.FactorStatusEnabled},
		{FactorID: "cci", Name: "Cci", SourceHash: "h2", Periods: []int{14}, LookbackBars: 200, Status: domain.FactorStatusEnabled},
	}, "/factor")
	require.NoError(t, err)
	require.Equal(t, 200, task.LookbackBars)
	require.Len(t, task.Factors, 2)
	require.Equal(t, []string{"funding_rate"}, task.Factors[0].Depends)
}

func TestBuildTaskRejectsInvalidInputs(t *testing.T) {
	_, err := BuildTask(TaskScope{}, nil, "/factor")
	require.Error(t, err)
}
