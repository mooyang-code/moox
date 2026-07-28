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
		{FactorID: "bias", Name: "Bias", SourceHash: "h1", InputColumns: []string{"close", "funding_rate"}, Outputs: []string{"bias"}, ParamsJSON: `{}`, LookbackRows: 100, Status: domain.FactorStatusEnabled},
		{FactorID: "cci", Name: "Cci", SourceHash: "h2", InputColumns: []string{"high", "low", "close"}, Outputs: []string{"cci"}, ParamsJSON: `{}`, LookbackRows: 200, Status: domain.FactorStatusEnabled},
	}, "/factor")
	require.NoError(t, err)
	require.Equal(t, 200, task.LookbackRows)
	require.Len(t, task.Factors, 2)
	require.Equal(t, []string{"close", "funding_rate"}, task.Factors[0].InputColumns)
	require.Equal(t, []string{"bias"}, task.Factors[0].Outputs)
}

func TestBuildTaskRejectsDuplicateOutputs(t *testing.T) {
	scope := TaskScope{SubjectID: "BTC", StartTime: time.Unix(1, 0), EndTime: time.Unix(2, 0)}
	_, err := BuildTask(scope, []domain.FactorDef{
		{FactorID: "a", Name: "A", SourceHash: "h1", Outputs: []string{"shared"}, Status: domain.FactorStatusEnabled},
		{FactorID: "b", Name: "B", SourceHash: "h2", Outputs: []string{"shared"}, Status: domain.FactorStatusEnabled},
	}, "/factor")
	require.ErrorContains(t, err, "shared")
}

func TestBuildTaskRejectsInvalidInputs(t *testing.T) {
	_, err := BuildTask(TaskScope{}, nil, "/factor")
	require.Error(t, err)
}
