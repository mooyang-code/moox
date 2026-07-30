package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

func TestSchedulerRejectsNewScopeWhenQueueIsFull(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
	require.NoError(t, svc.Enqueue(context.Background(), oneBarTask("BTC", time.Unix(1, 0))))
	err := svc.Enqueue(context.Background(), oneBarTask("ETH", time.Unix(1, 0)))
	require.ErrorIs(t, err, ErrQueueFull)
	require.EqualValues(t, 1, svc.Status().QueueOverflowCount)
}

func TestSchedulerSupersedeDoesNotGrowQueue(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
	require.NoError(t, svc.Enqueue(context.Background(), oneBarTask("BTC", time.Unix(1, 0))))
	require.NoError(t, svc.Enqueue(context.Background(), oneBarTask("BTC", time.Unix(2, 0))))
	require.Equal(t, 1, svc.Status().QueueDepth)
}

func TestSchedulerDoesNotMergeDifferentFactorsInSameDatasetScope(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 2}, nil, nil)
	first := oneBarTask("BTC", time.Unix(1, 0))
	second := oneBarTask("BTC", time.Unix(1, 0))
	second.Factor.FactorID = "cci"
	require.NoError(t, svc.Enqueue(context.Background(), first))
	require.NoError(t, svc.Enqueue(context.Background(), second))
	require.Equal(t, 2, svc.Status().QueueDepth)
}

func TestSchedulerSupersedeMergesPendingRanges(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
	first := rangeTask("BTC", time.Unix(10, 0), time.Unix(20, 0))
	first.TaskID = "first"
	first.LookbackPeriods = 2
	first.Factor = engine.FactorSpec{FactorID: "bias", Name: "Old", Outputs: []string{"old"}}
	second := rangeTask("BTC", time.Unix(5, 0), time.Unix(15, 0))
	second.TaskID = "second"
	second.LookbackPeriods = 7
	second.Factor = engine.FactorSpec{FactorID: "bias", Name: "New", Outputs: []string{"new"}}
	third := rangeTask("BTC", time.Unix(12, 0), time.Unix(30, 0))
	third.TaskID = "third"
	third.LookbackPeriods = 11
	third.Factor = engine.FactorSpec{FactorID: "bias", Name: "Latest", Outputs: []string{"latest"}}

	require.NoError(t, svc.Enqueue(context.Background(), first))
	require.NoError(t, svc.Enqueue(context.Background(), second))
	require.NoError(t, svc.Enqueue(context.Background(), third))
	queued, ok := svc.popShard(0, false)
	require.True(t, ok)
	require.Equal(t, time.Unix(5, 0), queued.StartTime)
	require.Equal(t, time.Unix(30, 0), queued.EndTime)
	require.Equal(t, 11, queued.LookbackPeriods)
	require.Equal(t, third.Factor, queued.Factor)
	require.Equal(t, DeterministicTaskID(queued), queued.TaskID)
}

func TestSchedulerDoesNotMergeNewEventIntoRunningTask(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
	running := rangeTask("BTC", time.Unix(10, 0), time.Unix(20, 0))
	next := rangeTask("BTC", time.Unix(5, 0), time.Unix(30, 0))
	next.TaskID = "next"

	require.NoError(t, svc.Enqueue(context.Background(), running))
	popped, ok := svc.popShard(0, true)
	require.True(t, ok)
	require.Equal(t, running.StartTime, popped.StartTime)
	require.NoError(t, svc.Enqueue(context.Background(), next))
	queued, ok := svc.popShard(0, false)
	require.True(t, ok)
	require.Equal(t, next.StartTime, queued.StartTime)
	require.Equal(t, next.EndTime, queued.EndTime)
	require.Equal(t, "next", queued.TaskID)
	svc.running.Add(-1)
}

func TestSchedulerMergedTaskIDDescribesFinalUnionAndIgnoresArrivalOrder(t *testing.T) {
	first := rangeTask("BTC", time.Unix(10, 0), time.Unix(20, 0))
	first.TaskID = DeterministicTaskID(first)
	second := rangeTask("BTC", time.Unix(5, 0), time.Unix(15, 0))
	second.TaskID = DeterministicTaskID(second)
	final := rangeTask("BTC", time.Unix(5, 0), time.Unix(20, 0))
	wantID := DeterministicTaskID(final)

	for _, tasks := range [][]Task{{first, second}, {second, first}} {
		svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
		for _, task := range tasks {
			require.NoError(t, svc.Enqueue(context.Background(), task))
		}
		queued, ok := svc.popShard(0, false)
		require.True(t, ok)
		require.Equal(t, final.StartTime, queued.StartTime)
		require.Equal(t, final.EndTime, queued.EndTime)
		require.Equal(t, wantID, queued.TaskID)
	}
	require.NotEqual(t, first.TaskID, wantID)
	require.NotEqual(t, second.TaskID, wantID)
}

func TestSchedulerMergedTaskIDUsesLatestExecutableFactorSnapshot(t *testing.T) {
	old := rangeTask("BTC", time.Unix(10, 0), time.Unix(20, 0))
	old.LookbackPeriods = 2
	old.Factor = engine.FactorSpec{
		FactorID: "factor", Name: "Factor", SourceHash: "old-hash", SourcePath: "/old/module.py",
		InputColumns: []string{"old_input"}, Outputs: []string{"old_output"}, ParamsJSON: `{"version":1}`,
	}
	old.TaskID = DeterministicTaskID(old)
	latest := rangeTask("BTC", time.Unix(5, 0), time.Unix(15, 0))
	latest.LookbackPeriods = 7
	latest.Factor = engine.FactorSpec{
		FactorID: "factor", Name: "FactorV2", SourceHash: "new-hash", SourcePath: "/new/module.py",
		InputColumns: []string{"input_b", "input_a"}, Outputs: []string{"output_b", "output_a"}, ParamsJSON: `{"version":2}`,
	}
	latest.TaskID = DeterministicTaskID(latest)
	want := latest
	want.EndTime = old.EndTime
	want.TaskID = DeterministicTaskID(want)
	oldSnapshotAtFinalRange := old
	oldSnapshotAtFinalRange.StartTime = latest.StartTime

	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
	require.NoError(t, svc.Enqueue(context.Background(), old))
	require.NoError(t, svc.Enqueue(context.Background(), latest))
	queued, ok := svc.popShard(0, false)
	require.True(t, ok)
	require.Equal(t, want.TaskID, queued.TaskID)
	require.NotEqual(t, old.TaskID, queued.TaskID)
	require.NotEqual(t, latest.TaskID, queued.TaskID)
	require.NotEqual(t, DeterministicTaskID(oldSnapshotAtFinalRange), queued.TaskID)
}

func TestDeterministicTaskIDCoversFactorSnapshotAndNormalizesSliceOrder(t *testing.T) {
	base := rangeTask("BTC", time.Unix(5, 0), time.Unix(20, 0))
	base.LookbackPeriods = 7
	base.Factor = engine.FactorSpec{
		FactorID: "factor", Name: "Factor", SourceHash: "hash", SourcePath: "/module.py",
		InputColumns: []string{"input_a", "input_b"}, Outputs: []string{"output_a", "output_b"}, ParamsJSON: `{"version":1}`,
	}
	baseID := DeterministicTaskID(base)

	mutations := map[string]func(*Task){
		"lookback":    func(task *Task) { task.LookbackPeriods++ },
		"factor id":   func(task *Task) { task.Factor.FactorID = "changed" },
		"name":        func(task *Task) { task.Factor.Name = "Changed" },
		"source hash": func(task *Task) { task.Factor.SourceHash = "changed" },
		"source path": func(task *Task) { task.Factor.SourcePath = "/changed.py" },
		"inputs":      func(task *Task) { task.Factor.InputColumns = []string{"different"} },
		"outputs":     func(task *Task) { task.Factor.Outputs = []string{"different"} },
		"params":      func(task *Task) { task.Factor.ParamsJSON = `{"version":2}` },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			require.NotEqual(t, baseID, DeterministicTaskID(changed))
		})
	}

	reordered := base
	reordered.Factor.InputColumns = []string{"input_b", "input_a"}
	reordered.Factor.Outputs = []string{"output_b", "output_a"}
	require.Equal(t, baseID, DeterministicTaskID(reordered))
}

func TestSchedulerAcceptsConfiguredNumberOfScopes(t *testing.T) {
	svc := NewService(Config{Workers: 4, QueueCapacity: 2000}, nil, nil)
	for i := 0; i < 2000; i++ {
		require.NoError(t, svc.Enqueue(context.Background(), oneBarTask(
			fmt.Sprintf("subject-%d", i), time.Unix(1, 0),
		)))
	}
	require.Equal(t, 2000, svc.Status().QueueDepth)
	require.ErrorIs(t, svc.Enqueue(context.Background(), oneBarTask("overflow", time.Unix(1, 0))), ErrQueueFull)
}

func TestValidateFactorResultNullAndFiniteContract(t *testing.T) {
	start := time.Unix(1, 0).UTC()
	end := time.Unix(3, 0).UTC()
	spec := engine.FactorSpec{Name: "Cci", Outputs: []string{"cci"}}
	require.NoError(t, validateFactorResult(spec, start, end, &engine.FactorResult{
		Rows: []engine.FactorResultRow{
			{DataTime: start, SeriesTag: "", Values: map[string]any{"cci": nil}},
			{DataTime: time.Unix(2, 0).UTC(), SeriesTag: "venue:binance", Values: map[string]any{"cci": 1.25}},
		},
	}))
	for _, value := range []any{"bad", math.NaN(), math.Inf(1)} {
		require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{
			Rows: []engine.FactorResultRow{{
				DataTime: start, Values: map[string]any{"cci": value},
			}},
		}))
	}
	require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{
		Rows: []engine.FactorResultRow{{
			DataTime: start, Values: map[string]any{"extra": 1.0},
		}},
	}))
	require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{
		Rows: []engine.FactorResultRow{{
			DataTime: end, Values: map[string]any{"cci": 1.0},
		}},
	}))
	require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{
		Rows: []engine.FactorResultRow{
			{DataTime: start, Values: map[string]any{"cci": 1.0}},
			{DataTime: start, Values: map[string]any{"cci": 2.0}},
		},
	}))
}

func TestRunChunksRangeAndWritesInOrder(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	firstTimes := makeTimes(base, 2000)
	secondTimes := makeTimes(base.Add(2000*time.Minute), 1)
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{
		frameChunk(firstTimes), frameChunk(secondTimes),
	}}
	exec := &fakeExecutor{}
	svc := NewService(Config{Workers: 1}, storage, exec)
	task := oneBarTask("BTC", base)
	task.EndTime = base.Add(3000 * time.Minute)
	require.NoError(t, svc.Run(context.Background(), task))
	require.Equal(t, 2, exec.calls)
	require.Equal(t, []int{2000, 1}, storage.writeSizes)
}

func TestRunAcceptsCompleteEmptyRange(t *testing.T) {
	exec := &fakeExecutor{}
	svc := NewService(Config{}, &fakeStorage{chunks: []*storageio.RangeChunk{{
		Frame: &engine.DataFrame{}, Complete: true,
	}}}, exec)
	require.NoError(t, svc.Run(context.Background(), oneBarTask("BTC", time.Unix(1, 0))))
	require.Zero(t, exec.calls)
}

func TestEventRunRetriesIncompleteEmptyRead(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{
		{Frame: &engine.DataFrame{}, Complete: false},
		frameChunk([]time.Time{base}),
	}}
	exec := &fakeExecutor{}
	svc := NewService(Config{EventReadRetry: 1}, storage, exec)

	require.NoError(t, svc.Run(context.Background(), oneBarTask("BTC", base)))
	require.Equal(t, 2, storage.readCalls)
	require.Equal(t, 1, exec.calls)
}

func TestEventRunDoesNotExecuteIncompleteRows(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	incomplete := frameChunk([]time.Time{base})
	incomplete.Complete = false
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{
		incomplete,
		frameChunk([]time.Time{base}),
	}}
	exec := &fakeExecutor{}
	svc := NewService(Config{EventReadRetry: 1}, storage, exec)

	require.NoError(t, svc.Run(context.Background(), oneBarTask("BTC", base)))
	require.Equal(t, 2, storage.readCalls)
	require.Equal(t, 1, exec.calls)
}

func TestManualRunAcceptsIncompleteEmptyRangeWithoutEventRetry(t *testing.T) {
	task := oneBarTask("BTC", time.Unix(1, 0))
	task.TriggerType = "manual"
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{{
		Frame: &engine.DataFrame{}, Complete: false,
	}}}
	exec := &fakeExecutor{}
	svc := NewService(Config{EventReadRetry: 3}, storage, exec)

	require.NoError(t, svc.Run(context.Background(), task))
	require.Equal(t, 1, storage.readCalls)
	require.Zero(t, exec.calls)
}

func TestManualRunRejectsIncompleteNonEmptyRangeWithoutRetryOrWrite(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	incomplete := frameChunk([]time.Time{base})
	incomplete.Complete = false
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{incomplete}}
	exec := &fakeExecutor{}
	task := oneBarTask("BTC", base)
	task.TriggerType = "recalc"
	svc := NewService(Config{EventReadRetry: 3}, storage, exec)

	require.ErrorIs(t, svc.Run(context.Background(), task), ErrViewIncomplete)
	require.Equal(t, 1, storage.readCalls)
	require.Zero(t, exec.calls)
	require.Empty(t, storage.writeSizes)
}

func TestEventRunReportsIncompleteAfterRetryExhaustionAndCanRunAgain(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{
		{Frame: &engine.DataFrame{}, Complete: false},
		{Frame: &engine.DataFrame{}, Complete: false},
		frameChunk([]time.Time{base}),
	}}
	exec := &fakeExecutor{}
	observer := &recordingDatasetObserver{}
	svc := NewService(
		Config{EventReadRetry: 1},
		storage,
		exec,
		WithDatasetMetrics(observer),
	)

	require.ErrorIs(t, svc.Run(context.Background(), oneBarTask("BTC", base)), ErrViewIncomplete)
	require.Len(t, observer.observations, 1)
	require.Equal(t, "incomplete", observer.observations[0].Result)
	require.NoError(t, svc.Run(context.Background(), oneBarTask("BTC", base)))
	require.Equal(t, 1, exec.calls)
}

func TestEventRunExpandsCorrectionThroughDependentPeriods(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	expandedEnd := base.Add(3 * time.Minute)
	storage := &fakeStorage{
		chunks: []*storageio.RangeChunk{
			frameChunk([]time.Time{base, base.Add(time.Minute), base.Add(2 * time.Minute)}),
		},
		expandedEnd: expandedEnd,
	}
	exec := &fakeExecutor{}
	svc := NewService(Config{}, storage, exec)
	task := oneBarTask("BTC", base)
	task.LookbackPeriods = 3

	require.NoError(t, svc.Run(context.Background(), task))
	require.Equal(t, 1, storage.expandCalls)
	require.Equal(t, expandedEnd, storage.readEnds[0])
	require.Equal(t, []int{3}, storage.writeSizes)
}

func TestEventRunRetriesIncompleteCorrectionExpansionBeforeFixingEnd(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	storage := &fakeStorage{
		expansions: []*storageio.EndExpansion{
			{EndTime: base.Add(2 * time.Minute), Complete: false, IndexedTo: base.Add(time.Minute)},
			{EndTime: base.Add(3 * time.Minute), Complete: true, IndexedTo: base.Add(2 * time.Minute)},
		},
		chunks: []*storageio.RangeChunk{
			frameChunk([]time.Time{base, base.Add(time.Minute), base.Add(2 * time.Minute)}),
		},
	}
	svc := NewService(Config{EventReadRetry: 1}, storage, &fakeExecutor{})
	task := oneBarTask("BTC", base)
	task.LookbackPeriods = 3

	require.NoError(t, svc.Run(context.Background(), task))
	require.Equal(t, 2, storage.expandCalls)
	require.Equal(t, base.Add(3*time.Minute), storage.readEnds[0])
	require.Equal(t, []int{3}, storage.writeSizes)
}

func TestEventRunAcceptsCurrentTailOnlyAfterExpansionRetries(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	eventEnd := base.Add(time.Nanosecond)
	storage := &fakeStorage{
		expansions: []*storageio.EndExpansion{
			{EndTime: eventEnd, Complete: false, IndexedTo: base},
			{EndTime: eventEnd, Complete: false, IndexedTo: base},
		},
		chunks: []*storageio.RangeChunk{frameChunk([]time.Time{base})},
	}
	svc := NewService(Config{EventReadRetry: 1}, storage, &fakeExecutor{})
	task := oneBarTask("BTC", base)
	task.LookbackPeriods = 3

	require.NoError(t, svc.Run(context.Background(), task))
	require.Equal(t, 2, storage.expandCalls)
	require.Equal(t, []int{1}, storage.writeSizes)
}

func TestRunSecondChunkFailureLeavesFirstChunkWritten(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{
		frameChunk(makeTimes(base, 2000)),
		frameChunk(makeTimes(base.Add(2000*time.Minute), 1)),
	}}
	exec := &fakeExecutor{failAt: 2}
	svc := NewService(Config{}, storage, exec)
	task := oneBarTask("BTC", base)
	task.EndTime = base.Add(3000 * time.Minute)
	require.Error(t, svc.Run(context.Background(), task))
	require.Equal(t, []int{2000}, storage.writeSizes)
}

func TestRunRetriesTransientExecutorFailure(t *testing.T) {
	base := time.Unix(0, 0).UTC()
	times := makeTimes(base, 1)
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk(times)}, repeatFirst: true}
	exec := &fakeExecutor{transientFailures: 1}
	svc := NewService(Config{MaxRetry: 1}, storage, exec)
	require.NoError(t, svc.Run(context.Background(), oneBarTask("BTC", base)))
	require.Equal(t, 2, exec.calls)
	require.Equal(t, []int{1}, storage.writeSizes)
}

func TestRunHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := NewService(Config{}, &fakeStorage{}, &fakeExecutor{})
	require.ErrorIs(t, svc.Run(ctx, oneBarTask("BTC", time.Unix(1, 0))), context.Canceled)
}

type recordingDatasetObserver struct {
	observations []report.DatasetObservation
}

func (o *recordingDatasetObserver) ObserveRun(observation report.DatasetObservation) error {
	o.observations = append(o.observations, observation)
	return nil
}

func TestRunObservesWatermarkOnlyAfterFactorPatchCommit(t *testing.T) {
	base := time.Date(2026, 7, 28, 10, 3, 0, 0, time.UTC)
	targetTimes := []time.Time{base, base.Add(2 * time.Minute)}
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk(targetTimes)}}
	observer := &recordingDatasetObserver{}
	svc := NewService(Config{}, storage, &fakeExecutor{}, WithDatasetMetrics(observer))

	require.NoError(t, svc.Run(context.Background(), oneBarTask("BTC", base)))
	require.Len(t, observer.observations, 1)
	observation := observer.observations[0]
	require.Equal(t, report.DatasetKey{
		SpaceID: "crypto", DatasetID: "bars_factor", Freq: "1m",
	}, observation.Key)
	require.Equal(t, "success", observation.Result)
	require.EqualValues(t, 2, observation.Rows)
	require.Equal(t, targetTimes[1], observation.InputWatermark)
	require.Equal(t, targetTimes[1], observation.OutputWatermark)
}

func TestMaxTimeDoesNotAssumeTargetOrder(t *testing.T) {
	base := time.Date(2026, 7, 28, 10, 3, 0, 0, time.UTC)
	require.Equal(t, base.Add(2*time.Minute), maxTime([]time.Time{
		base.Add(time.Minute), base.Add(2 * time.Minute), base,
	}))
}

func TestRunWriteFailureDoesNotAdvanceFactorWatermark(t *testing.T) {
	base := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	storage := &fakeStorage{
		chunks:   []*storageio.RangeChunk{frameChunk([]time.Time{base})},
		writeErr: errors.New("storage unavailable"),
	}
	observer := &recordingDatasetObserver{}
	svc := NewService(Config{}, storage, &fakeExecutor{}, WithDatasetMetrics(observer))

	require.Error(t, svc.Run(context.Background(), oneBarTask("BTC", base)))
	require.Len(t, observer.observations, 1)
	observation := observer.observations[0]
	require.Equal(t, "error", observation.Result)
	require.True(t, observation.InputWatermark.IsZero())
	require.True(t, observation.OutputWatermark.IsZero())
}

func oneBarTask(subject string, at time.Time) Task {
	return rangeTask(subject, at, at.Add(time.Nanosecond))
}

func rangeTask(subject string, start, end time.Time) Task {
	return Task{FactorTask: engine.FactorTask{
		TaskID: "task-" + subject, SpaceID: "crypto",
		SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: subject, Freq: "1m", StartTime: start, EndTime: end,
		LookbackPeriods: 20,
		Factor:          engine.FactorSpec{FactorID: "bias", Name: "Bias", Outputs: []string{"bias"}},
	}, TriggerType: "event"}
}

type fakeStorage struct {
	chunks      []*storageio.RangeChunk
	writeSizes  []int
	writeErr    error
	repeatFirst bool
	expandedEnd time.Time
	expansions  []*storageio.EndExpansion
	expandErr   error
	expandCalls int
	readCalls   int
	readEnds    []time.Time
}

func (s *fakeStorage) ReadRangeChunk(
	_ context.Context,
	_ storageio.WindowKey,
	_ time.Time,
	end time.Time,
	_ int,
	_ int,
	_ []string,
) (*storageio.RangeChunk, error) {
	s.readCalls++
	s.readEnds = append(s.readEnds, end)
	if len(s.chunks) == 0 {
		return &storageio.RangeChunk{Frame: &engine.DataFrame{}, Complete: true}, nil
	}
	chunk := s.chunks[0]
	if !s.repeatFirst {
		s.chunks = s.chunks[1:]
	}
	return chunk, nil
}
func (s *fakeStorage) WriteFactorPatch(_ context.Context, _ *engine.FactorTask, result *engine.FactorResult) (uint64, error) {
	s.writeSizes = append(s.writeSizes, len(result.Rows))
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return uint64(len(result.Rows)), nil
}

func (s *fakeStorage) ExpandEndByPeriods(
	_ context.Context,
	_ storageio.WindowKey,
	end time.Time,
	_ int,
) (*storageio.EndExpansion, error) {
	s.expandCalls++
	if s.expandErr != nil {
		return nil, s.expandErr
	}
	if len(s.expansions) > 0 {
		expansion := s.expansions[0]
		s.expansions = s.expansions[1:]
		return expansion, nil
	}
	if s.expandedEnd.After(end) {
		end = s.expandedEnd
	}
	return &storageio.EndExpansion{EndTime: end, Complete: true}, nil
}

type fakeExecutor struct {
	calls             int
	err               error
	failAt            int
	transientFailures int
}

func (e *fakeExecutor) Execute(_ context.Context, _ *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.calls++
	if e.transientFailures > 0 {
		e.transientFailures--
		return nil, errors.New("transient worker failure")
	}
	if e.failAt > 0 && e.calls == e.failAt {
		return nil, engine.NonRetryableError{Err: errors.New("planned executor failure")}
	}
	if e.err != nil {
		return nil, e.err
	}
	rows := make([]engine.FactorResultRow, 0, len(frame.DataTimes))
	for i, dataTime := range frame.DataTimes {
		tag := ""
		if i < len(frame.SeriesTags) {
			tag = frame.SeriesTags[i]
		}
		rows = append(rows, engine.FactorResultRow{
			DataTime: dataTime, SeriesTag: tag, Values: map[string]any{"bias": nil},
		})
	}
	return &engine.FactorResult{Rows: rows}, nil
}
func (*fakeExecutor) Close() error { return nil }

func makeTimes(start time.Time, count int) []time.Time {
	out := make([]time.Time, count)
	for i := range out {
		out[i] = start.Add(time.Duration(i) * time.Minute)
	}
	return out
}
func frameChunk(times []time.Time) *storageio.RangeChunk {
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			DataTimes: times, SeriesTags: make([]string, len(times)),
		},
		TargetPeriods: times, Complete: true,
	}
}
