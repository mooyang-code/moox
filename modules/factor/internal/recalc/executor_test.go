package recalc

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

type progressRecord struct {
	progress time.Time
	outcome  pipeline.Outcome
}

func executorSelection(subjects ...string) Selection {
	member := testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3)
	return Selection{
		Set: domain.FactorSet{
			SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "dataset_bars_1m", Freq: "1m",
			ResultDatasetID: "dataset_factor_bars_1m", Status: domain.SetStatusEnabled,
		},
		Factors: []domain.FactorDef{member.def}, Subjects: subjects, Columns: []string{"close"},
	}
}

func fixedSource(selection Selection) SelectionSource {
	return func(context.Context) (Selection, error) { return selection, nil }
}

func recordProgress(records *[]progressRecord, stopAfter int) ProgressFunc {
	return func(_ context.Context, progress time.Time, outcome pipeline.Outcome) (bool, error) {
		*records = append(*records, progressRecord{progress: progress, outcome: outcome})
		return stopAfter > 0 && len(*records) >= stopAfter, nil
	}
}

func testWindow(periods int) Window {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Window{Start: start, End: start.Add(time.Duration(periods) * time.Minute)}
}

func TestExecutorRunsChunksAndReportsProgress(t *testing.T) {
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(2))
	window := testWindow(6)
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0)))

	plans := runner.snapshot()
	require.Len(t, plans, 3)
	require.Len(t, records, 3)
	for i, record := range records {
		want := window.Start.Add(time.Duration(2*(i+1)) * time.Minute)
		require.Equal(t, want, record.progress)
		require.Equal(t, pipeline.ModeRecalc, plans[i].Mode)
		require.Equal(t, want, plans[i].TargetEnd)
	}
	require.Equal(t, window.End, records[len(records)-1].progress)
}

func TestExecutorStopsWhenProgressReportsCancelled(t *testing.T) {
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(2))
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), testWindow(6), fixedSource(executorSelection("BTC")), recordProgress(&records, 1)))

	require.Len(t, runner.snapshot(), 1)
	require.Len(t, records, 1)
}

func TestExecutorResumesFromProgress(t *testing.T) {
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(2))
	window := testWindow(6)
	window.Progress = window.Start.Add(4 * time.Minute)
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0)))

	plans := runner.snapshot()
	require.Len(t, plans, 1)
	require.Equal(t, window.Progress, plans[0].TargetStart)
	require.Equal(t, []progressRecord{{progress: window.End, outcome: pipeline.Outcome{Status: "complete"}}}, records)
}

func TestExecutorEmptySubjectsSucceedsImmediately(t *testing.T) {
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(2))
	window := testWindow(6)
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), window, fixedSource(executorSelection()), recordProgress(&records, 0)))

	require.Empty(t, runner.snapshot())
	require.Equal(t, []progressRecord{{progress: window.End}}, records)
}

func TestExecutorCompletedWindowReportsEnd(t *testing.T) {
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}))
	window := testWindow(6)
	window.Progress = window.End
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), window, fixedSource(executorSelection("BTC")), recordProgress(&records, 0)))

	require.Empty(t, runner.snapshot())
	require.Equal(t, []progressRecord{{progress: window.End}}, records)
}

func TestExecutorRetriesChunkThenFails(t *testing.T) {
	attempts := 0
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		attempts++
		return pipeline.Outcome{}, fmt.Errorf("%w: storage unavailable", storageio.ErrInfra)
	}}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkRetry(3, time.Millisecond))
	var records []progressRecord

	err := executor.Run(context.Background(), testWindow(6), fixedSource(executorSelection("BTC")), recordProgress(&records, 0))

	require.ErrorIs(t, err, storageio.ErrInfra)
	require.Equal(t, 3, attempts)
	require.Empty(t, records)
}

func TestExecutorPermanentFailureIsNotRetried(t *testing.T) {
	attempts := 0
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		attempts++
		return pipeline.Outcome{}, errors.New("bad factor output")
	}}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkRetry(3, time.Millisecond))
	var records []progressRecord

	require.Error(t, executor.Run(context.Background(), testWindow(6), fixedSource(executorSelection("BTC")), recordProgress(&records, 0)))
	require.Equal(t, 1, attempts)
}

func TestExecutorPassesOutcomeToReport(t *testing.T) {
	degraded := pipeline.Outcome{Status: "degraded", FailedSubjects: []string{"ETH"}}
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) { return degraded, nil }}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}))
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), testWindow(6), fixedSource(executorSelection("BTC", "ETH")), recordProgress(&records, 0)))

	require.Len(t, records, 1)
	require.Equal(t, degraded, records[0].outcome)
	require.Equal(t, "degraded: failed_subjects=1", DegradedNote(records[0].outcome))
}

func TestExecutorHoldsSetLockPerChunk(t *testing.T) {
	locks := &countingLocks{}
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		require.True(t, locks.held, "chunk must run under the set lock")
		return pipeline.Outcome{Status: "complete"}, nil
	}}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(2), WithLocks(locks))
	var records []progressRecord

	require.NoError(t, executor.Run(context.Background(), testWindow(6), fixedSource(executorSelection("BTC")), recordProgress(&records, 0)))

	require.Equal(t, []string{"fset_bars_1m", "fset_bars_1m", "fset_bars_1m"}, locks.setIDs)
	require.False(t, locks.held)
}

type countingLocks struct {
	setIDs []string
	held   bool
}

func (l *countingLocks) LockContext(_ context.Context, setID string) (func(), error) {
	l.setIDs = append(l.setIDs, setID)
	l.held = true
	return func() { l.held = false }, nil
}
