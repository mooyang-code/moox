package taskrunner

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestBuildTaskUsesExactlyOneFactor(t *testing.T) {
	task, err := BuildTask(TaskScope{
		TaskID: "task-1", TriggerType: "recalc", SpaceID: "crypto",
		SourceDataset: "bars", TargetDataset: "bars_factor", SubjectID: "BTC",
		Freq: "1m", StartTime: time.Unix(1, 0), EndTime: time.Unix(3, 0),
	}, domain.FactorDef{
		FactorID: "bias", Name: "Bias", SourceHash: "h1",
		InputColumns: []string{"close", "funding_rate"}, Outputs: []string{"bias"},
		ParamsJSON: `{}`, LookbackPeriods: 100, Status: domain.FactorStatusEnabled,
	}, "/factor")
	require.NoError(t, err)
	require.Equal(t, 100, task.LookbackPeriods)
	require.Equal(t, "bias", task.Factor.FactorID)
	require.Equal(t, []string{"close", "funding_rate"}, task.Factor.InputColumns)
	require.Equal(t, []string{"bias"}, task.Factor.Outputs)
}

func TestBuildTaskRejectsInvalidInputs(t *testing.T) {
	_, err := BuildTask(TaskScope{}, domain.FactorDef{}, "/factor")
	require.Error(t, err)
}

func TestBuildTaskRejectsDisabledFactor(t *testing.T) {
	_, err := BuildTask(TaskScope{
		SubjectID: "BTC", StartTime: time.Unix(1, 0), EndTime: time.Unix(2, 0),
	}, domain.FactorDef{FactorID: "f", SourceHash: "hash", Status: domain.FactorStatusDisabled}, "/factor")
	require.ErrorContains(t, err, "not enabled")
}
