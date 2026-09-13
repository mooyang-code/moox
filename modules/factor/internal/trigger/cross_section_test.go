package trigger

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

func TestCrossSectionReadyIgnoresFactorPeriodComputed(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	ready := testReady(time.Date(2026, 9, 13, 1, 0, 0, 0, time.UTC), "complete", "")
	ready.CompletionKind = events.FactorPeriodComputed.Name()
	require.NoError(t, executor.Execute(context.Background(), "space", "factor-ready", ready))
	require.Empty(t, runner.tasks)
	require.Nil(t, storage.getMarker())
}

func TestCrossSectionReadyIgnoresOtherViewAndPeriod(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	period := time.Date(2026, 9, 13, 1, 1, 0, 0, time.UTC)
	otherView := testReady(period, "complete", "")
	otherView.ViewId = "other_view"
	require.NoError(t, executor.Execute(context.Background(), "space", "other-view", otherView))
	otherPeriod := testReady(period, "complete", "")
	otherPeriod.Frequency = "5m"
	require.NoError(t, executor.Execute(context.Background(), "space", "other-freq", otherPeriod))
	require.Empty(t, runner.tasks)
	require.Nil(t, storage.getMarker())
}

func TestCrossSectionReadyRejectsMissingInputs(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir(),
		WithViewColumns(staticViewColumns{"source_view": []string{"volume"}}))
	err := executor.Execute(context.Background(), "space", "merge-ready", testReady(time.Date(2026, 9, 13, 1, 2, 0, 0, time.UTC), "complete", ""))
	require.ErrorIs(t, err, ErrMissingBindingInputs)
	require.Empty(t, runner.tasks)
	require.Nil(t, storage.getMarker())
}

func TestCrossSectionReadySkipsIncompletePanelByDefault(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir())
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-degraded", testReady(time.Date(2026, 9, 13, 1, 3, 0, 0, time.UTC), "degraded", "ETH")))
	require.Empty(t, runner.tasks)
	marker := storage.getMarker()
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, marker.GetBindings()[0].GetSkippedSubjects())
}

func TestCrossSectionReadyAllowsDegradedWithFailedSetInContext(t *testing.T) {
	bindings := periodBindings{rows: []domain.FactorBinding{{
		BindingID: "rank-binding", BindingGeneration: "incarnation-1", FactorID: "rank", SpaceID: "space",
		SourceViewID: "source_view", ResultDatasetID: "mdataset_binance_kline_1m", Freq: "1m",
		SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC","ETH","SOL"]`, Status: domain.BindingStatusEnabled,
	}}}
	factors := periodFactors{"rank": domain.FactorDef{
		FactorType: domain.FactorTypeCrossSection, FactorID: "rank", Name: "rank", SourceHash: "hash-rank",
		InputColumns: []string{"close"}, Outputs: []string{"rank"}, LookbackPeriods: 1,
		ParamsJSON: `{"allow_degraded":true}`, Status: domain.FactorStatusEnabled,
	}}
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(bindings, factors, runner, storage, t.TempDir())
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-degraded-allow", testReady(time.Date(2026, 9, 13, 1, 4, 0, 0, time.UTC), "degraded", "ETH")))
	require.Len(t, runner.tasks, 1)
	task := runner.tasks[0]
	require.Equal(t, "degraded", task.InputStatus)
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, task.ExpectedSubjects)
	require.Equal(t, []string{"BTC", "SOL"}, task.AvailableSubjects)
	require.Equal(t, []string{"ETH"}, task.MissingSubjects)
	marker := storage.getMarker()
	require.Equal(t, "degraded", marker.GetStatus())
	require.Equal(t, []string{"ETH"}, marker.GetBindings()[0].GetFailedSubjects())
}

func TestCrossSectionReadyRunsLockedViewPanel(t *testing.T) {
	runner := new(recordingCombinationRunner)
	storage := new(periodStorageFake)
	executor := NewViewReadyRunner(twoPeriodBindings(), twoPeriodFactors(), runner, storage, t.TempDir(),
		WithViewColumns(staticViewColumns{"source_view": []string{"close", "volume"}}))
	require.NoError(t, executor.Execute(context.Background(), "space", "merge-complete", testReady(time.Date(2026, 9, 13, 1, 5, 0, 0, time.UTC), "complete", "")))
	require.Len(t, runner.tasks, 2)
	for _, task := range runner.tasks {
		require.Equal(t, "view_ready", task.TriggerType)
		require.Equal(t, "result", task.ResultDatasetID)
		require.Equal(t, []string{"BTC", "ETH", "SOL"}, task.ExpectedSubjects)
		require.Equal(t, "complete", task.InputStatus)
		require.Equal(t, taskrunner.DeterministicTaskID(task), task.TaskID)
	}
	require.Equal(t, "complete", storage.getMarker().GetStatus())
}

type staticViewColumns map[string][]string

func (s staticViewColumns) Columns(_ context.Context, _, viewID string) ([]string, error) {
	return append([]string(nil), s[viewID]...), nil
}
