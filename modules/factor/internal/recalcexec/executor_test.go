package recalcexec

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

type recordingRunner struct {
	mu    sync.Mutex
	plans []pipeline.Plan
	onRun func(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

func (r *recordingRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	r.mu.Unlock()
	if r.onRun != nil {
		return r.onRun(ctx, plan)
	}
	return pipeline.Outcome{Status: "complete"}, nil
}

func (r *recordingRunner) snapshot() []pipeline.Plan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pipeline.Plan(nil), r.plans...)
}

func pipelineSelection(factors ...recalcMember) Selection {
	defs := make([]domain.FactorDef, 0, len(factors))
	for _, factor := range factors {
		defs = append(defs, factor.def)
	}
	return Selection{
		Set: domain.FactorSet{
			SetID: "fset_bars_1m", SpaceID: "crypto", SourceDatasetID: "dataset_bars_1m", Freq: "1m",
			SubjectMode: domain.SubjectModeInclude, Subjects: []string{"BTC"},
			ResultDatasetID: "dataset_factor_bars_1m", Status: domain.SetStatusEnabled,
		},
		Factors: defs, Subjects: []string{"BTC"}, Columns: []string{"close"},
	}
}

// 内存预算让分块在读取之前收窄；1 个周期也超限时补算以资源错误失败，不读取。
func TestChunkNarrowsToMemoryBudget(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	selection := pipelineSelection(testRecalcFactor("mean", domain.MemberStatusEnabled, 3))
	full, err := pipeline.EstimateChunkMemory(selection.Factors, len(selection.Subjects), 6, len(selection.Columns))
	require.NoError(t, err)
	fit, err := pipeline.MaxPeriodsWithin(selection.Factors, len(selection.Subjects), len(selection.Columns), 6, full.Total()/2)
	require.NoError(t, err)
	require.Less(t, fit, 6)

	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(6), WithMemoryBudget(full.Total()/2))
	var records []progressRecord
	err = executor.Run(context.Background(), Window{Start: start, End: start.Add(6 * time.Minute)}, fixedSource(selection), recordProgress(&records, 0))
	require.NoError(t, err)
	require.Greater(t, len(storage.readRequests), 1, "分块被收窄成多块")
	require.Equal(t, start.Add(time.Duration(fit)*time.Minute), storage.readRequests[0].End)
	require.Len(t, storage.written, 6)

	tiny := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithMemoryBudget(64))
	storage.readRequests = nil
	err = tiny.Run(context.Background(), Window{Start: start, End: start.Add(time.Minute)}, fixedSource(selection), recordProgress(&records, 0))
	require.ErrorIs(t, err, pipeline.ErrMemoryBudget)
	require.Empty(t, storage.readRequests)
}

func TestChunkReadRangeIncludesLookback(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(3))
	selection := pipelineSelection(
		testRecalcFactor("short", domain.MemberStatusEnabled, 2),
		testRecalcFactor("long", domain.MemberStatusEnabled, 3),
	)
	var records []progressRecord

	err := executor.Run(context.Background(), Window{Start: start, End: start.Add(3 * time.Minute)}, fixedSource(selection), recordProgress(&records, 0))

	require.NoError(t, err)
	require.Len(t, storage.readRequests, 1)
	require.Equal(t, start.Add(-2*time.Minute), storage.readRequests[0].Start)
	require.Equal(t, start.Add(3*time.Minute), storage.readRequests[0].End)
	require.Len(t, storage.written, 3)
	require.Equal(t, start, storage.written[0].DataTime)
	require.Equal(t, 7.0, storage.written[0].Fields["long_value"])
	require.Equal(t, 7.0, storage.written[0].Fields["short_value"])
}

func TestRecalcDoesNotReportMarker(t *testing.T) {
	storage := &recalcPipelineStore{}
	runner := pipeline.NewRunner(storage, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}))
	period := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)

	_, err := executor.RunChunk(context.Background(), pipelineSelection(testRecalcFactor("close_factor", domain.MemberStatusEnabled, 3)), period, period.Add(time.Minute))

	require.NoError(t, err)
	require.Equal(t, 0, storage.markerCalls)
}

type recalcPipelineStore struct {
	readRequests []storageio.ReadRequest
	markerCalls  int
	written      []storageio.ResultRow
}

func (*recalcPipelineStore) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (s *recalcPipelineStore) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	s.readRequests = append(s.readRequests, req)
	frame := &storageio.Frame{SubjectID: "BTC", Columns: []string{"data_time", "series_tag", "close"}}
	for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
		frame.Rows = append(frame.Rows, []any{at, "source", 7.0})
	}
	return map[string]*storageio.Frame{"BTC": frame}, nil
}

func (s *recalcPipelineStore) WriteRows(_ context.Context, _, _, _ string, rows []storageio.ResultRow) error {
	s.written = append(s.written, rows...)
	return nil
}

func (s *recalcPipelineStore) ReportComputed(context.Context, storageio.PeriodMarker) error {
	s.markerCalls++
	return nil
}

func (*recalcPipelineStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

// recalcMember is a test definition together with its member status.
type recalcMember struct {
	def    domain.FactorDef
	status string
}

func testRecalcFactor(id, status string, lookback int) recalcMember {
	return recalcMember{status: status, def: domain.MustDefinition(id, id,
		fmt.Sprintf("outputs:\n  %s_value: ts_mean(close, %d)\n", id, lookback))}
}

// RunChunkWith：先取集合锁再在锁内重读选择；after 在仍持有锁时调用；FillOnly 传到计划里并且不写 carry 列。
func TestRunChunkWithRereadsSelectionAndCallsAfterUnderLock(t *testing.T) {
	locks := &countingLocks{}
	runner := &recordingRunner{onRun: func(context.Context, pipeline.Plan) (pipeline.Outcome, error) {
		require.True(t, locks.held, "运行必须在集合锁内")
		return pipeline.Outcome{Status: "complete", Items: map[pipeline.ItemKey]pipeline.ItemStatus{{FactorID: "f", Subject: "BTC"}: pipeline.ItemWritten}}, nil
	}}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithLocks(locks))
	calls := 0
	source := func(context.Context) (Selection, error) {
		calls++
		selection := executorSelection("BTC")
		selection.FillOnly = true
		if locks.held {
			selection.Columns = []string{"close", "reloaded-under-lock"}
		}
		return selection, nil
	}
	afterHeld := false
	start := testWindow(1).Start

	outcome, err := executor.RunChunkWith(context.Background(), source, start, start.Add(2*time.Minute), func(selection Selection, outcome pipeline.Outcome, runErr error) {
		afterHeld = locks.held
		require.NoError(t, runErr)
		require.Equal(t, []string{"close", "reloaded-under-lock"}, selection.Columns)
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls, "锁外探测一次，锁内重读一次")
	require.True(t, afterHeld, "after 在持有集合锁时调用")
	require.Equal(t, pipeline.ItemWritten, outcome.Items[pipeline.ItemKey{FactorID: "f", Subject: "BTC"}])
	plans := runner.snapshot()
	require.Len(t, plans, 1)
	require.True(t, plans[0].FillOnly)
	require.False(t, plans[0].WriteCarry, "只填充不写 carry 列")
	require.Equal(t, []string{"close", "reloaded-under-lock"}, plans[0].CarryColumns, "源列仍读取（计算输入需要）")
	require.False(t, locks.held)
}

// 预热跳过的恢复：只填充同时写 carry 列（那一期实时路径没有写任何行）。
func TestRunChunkWithFillCarryWritesCarryColumns(t *testing.T) {
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}))
	source := func(context.Context) (Selection, error) {
		selection := executorSelection("BTC")
		selection.FillOnly, selection.FillCarry = true, true
		return selection, nil
	}
	start := testWindow(1).Start
	_, err := executor.RunChunkWith(context.Background(), source, start, start.Add(time.Minute), nil)
	require.NoError(t, err)
	plans := runner.snapshot()
	require.Len(t, plans, 1)
	require.True(t, plans[0].FillOnly)
	require.True(t, plans[0].WriteCarry)
}

// 锁内重读发现没有标的：不运行，也不调用 after。
func TestRunChunkWithSkipsWhenSelectionBecomesEmpty(t *testing.T) {
	locks := &countingLocks{}
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithLocks(locks))
	start := testWindow(1).Start
	calls := 0
	source := func(context.Context) (Selection, error) {
		calls++
		if calls == 1 {
			return executorSelection("BTC"), nil
		}
		return executorSelection(), nil // 等锁期间待办已被处理
	}
	afterCalled := false
	_, err := executor.RunChunkWith(context.Background(), source, start, start.Add(time.Minute), func(Selection, pipeline.Outcome, error) { afterCalled = true })
	require.NoError(t, err)
	require.Empty(t, runner.snapshot())
	require.False(t, afterCalled)
}
