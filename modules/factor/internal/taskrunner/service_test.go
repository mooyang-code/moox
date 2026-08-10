package taskrunner

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/stretchr/testify/require"
)

func TestRunSkipsTaskRejectedByCurrentDefinition(t *testing.T) {
	exec := &fakeExecutor{}
	runner := NewService(1, &fakeStorage{}, exec, WithTaskValidator(func(context.Context, Task) error {
		return ErrStaleTask
	}))

	err := runner.Run(context.Background(), oneBarTask("BTC", time.Unix(1, 0)))
	require.ErrorIs(t, err, ErrStaleTask)
	require.Zero(t, exec.callCount())
}

func TestRunHoldsFactorGateWhileValidatingAndExecuting(t *testing.T) {
	gate := NewFactorGate()
	releaseRead := make(chan struct{})
	storage := &blockingReadStorage{entered: make(chan struct{}, 1), release: releaseRead}
	runner := NewService(1, storage, &fakeExecutor{}, WithFactorGate(gate))
	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background(), oneBarTask("BTC", time.Unix(1, 0))) }()
	<-storage.entered

	mutated := make(chan struct{})
	go gate.Mutate("bias", func() { close(mutated) })
	select {
	case <-mutated:
		t.Fatal("factor mutation entered while task was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseRead)
	require.NoError(t, <-done)
	require.Eventually(t, func() bool {
		select {
		case <-mutated:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}

func TestValidateFactorResultNullAndFiniteContract(t *testing.T) {
	spec := engine.FactorSpec{Outputs: []string{"bias"}}
	start := time.Unix(1, 0).UTC()
	end := time.Unix(3, 0).UTC()
	require.NoError(t, validateFactorResult(spec, start, end, &engine.FactorResult{Rows: []engine.FactorResultRow{{
		DataTime: start, Values: map[string]any{"bias": nil},
	}}}))
	require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{Rows: []engine.FactorResultRow{{
		DataTime: start, Values: map[string]any{"bias": math.NaN()},
	}}}))
	require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{Rows: []engine.FactorResultRow{{
		DataTime: start, Values: map[string]any{"other": 1.0},
	}}}))
	require.Error(t, validateFactorResult(spec, start, end, &engine.FactorResult{Rows: []engine.FactorResultRow{
		{DataTime: start, SeriesTag: "spot", Values: map[string]any{"bias": 1.0}},
		{DataTime: start, SeriesTag: "spot", Values: map[string]any{"bias": 2.0}},
	}}))
}

func TestFilterTargetResultDropsLookbackRows(t *testing.T) {
	start := time.Unix(2, 0).UTC()
	end := time.Unix(3, 0).UTC()
	filtered := filterTargetResult(&engine.FactorResult{Rows: []engine.FactorResultRow{
		{DataTime: time.Unix(1, 0).UTC()}, {DataTime: start}, {DataTime: end},
	}}, start, end)
	require.Equal(t, []engine.FactorResultRow{{DataTime: start}}, filtered.Rows)
}

func TestRunChunksRangeAndWritesInOrder(t *testing.T) {
	base := time.Unix(60, 0).UTC()
	firstTimes := makeTimes(base, 2000)
	secondTimes := makeTimes(base.Add(2000*time.Minute), 1)
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk(firstTimes), frameChunk(secondTimes)}}
	exec := &fakeExecutor{}
	runner := NewService(1, storage, exec)
	task := rangeTask("BTC", base, base.Add(3000*time.Minute))
	task.TriggerType = "manual"

	require.NoError(t, runner.Run(context.Background(), task))
	require.Equal(t, 2, exec.callCount())
	require.Equal(t, []int{2000, 1}, storage.writeSizes)
	require.Equal(t, []int64{base.Unix(), secondTimes[0].Unix()}, storage.writePeriods)
}

func TestViewReadyEmptyRangeClearsPreviousOutput(t *testing.T) {
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{{Frame: &engine.DataFrame{}, Complete: true}}}
	task := oneBarTask("BTC", time.Unix(1, 0))
	task.TriggerType = "view_ready"
	runner := NewService(1, storage, &fakeExecutor{})

	require.NoError(t, runner.Run(context.Background(), task))
	require.Equal(t, []int{0}, storage.writeSizes)
}

func TestRunRetriesRetryableExecutorFailureOnce(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk([]time.Time{base})}, repeatFirst: true}
	exec := &fakeExecutor{transientFailures: 1}
	runner := NewService(1, storage, exec)

	require.NoError(t, runner.Run(context.Background(), oneBarTask("BTC", base)))
	require.Equal(t, 2, exec.callCount())
	require.Equal(t, []int{1}, storage.writeSizes)
}

func TestRunDoesNotRetryNonRetryableExecutorFailure(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk([]time.Time{base})}}
	exec := &fakeExecutor{err: engine.NonRetryableError{Err: errors.New("invalid factor")}}
	runner := NewService(1, storage, exec)

	require.Error(t, runner.Run(context.Background(), oneBarTask("BTC", base)))
	require.Equal(t, 1, exec.callCount())
}

func TestRunAppliesViewReadTimeout(t *testing.T) {
	storage := &timeoutReadStorage{}
	runner := NewService(1, storage, &fakeExecutor{}, WithViewReadConfig(1, 10*time.Millisecond))
	task := oneBarTask("BTC", time.Unix(1, 0))
	task.TriggerType = "manual"
	task.PeriodTime = 0

	err := runner.Run(context.Background(), task)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 2, storage.callCount())
}

func TestRunObservesWatermarkOnlyAfterFactorPatchCommit(t *testing.T) {
	base := time.Date(2026, 7, 28, 10, 3, 0, 0, time.UTC)
	targetTimes := []time.Time{base, base.Add(2 * time.Minute)}
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk(targetTimes)}}
	observer := &recordingDatasetObserver{}
	runner := NewService(1, storage, &fakeExecutor{}, WithDatasetMetrics(observer))

	require.NoError(t, runner.Run(context.Background(), oneBarTask("BTC", base)))
	require.Len(t, observer.observations, 1)
	require.Equal(t, report.DatasetKey{SpaceID: "crypto", DatasetID: "bars_factor", Freq: "1m"}, observer.observations[0].Key)
	require.Equal(t, "success", observer.observations[0].Result)
	require.Equal(t, targetTimes[1], observer.observations[0].OutputWatermark)
}

func TestRunWriteFailureDoesNotAdvanceFactorWatermark(t *testing.T) {
	base := time.Unix(1, 0).UTC()
	storage := &fakeStorage{chunks: []*storageio.RangeChunk{frameChunk([]time.Time{base})}, writeErr: errors.New("unavailable")}
	observer := &recordingDatasetObserver{}
	runner := NewService(1, storage, &fakeExecutor{}, WithDatasetMetrics(observer))

	require.Error(t, runner.Run(context.Background(), oneBarTask("BTC", base)))
	require.Len(t, observer.observations, 1)
	require.Equal(t, "error", observer.observations[0].Result)
	require.True(t, observer.observations[0].OutputWatermark.IsZero())
}

type recordingDatasetObserver struct {
	mu           sync.Mutex
	observations []report.DatasetObservation
}

func (o *recordingDatasetObserver) ObserveRun(observation report.DatasetObservation) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.observations = append(o.observations, observation)
	return nil
}

func oneBarTask(subject string, at time.Time) Task {
	return rangeTask(subject, at, at.Add(time.Nanosecond))
}

func rangeTask(subject string, start, end time.Time) Task {
	return Task{FactorTask: engine.FactorTask{
		TaskID: "task-" + subject, SpaceID: "crypto", SourceViewID: "bars",
		ResultDatasetID: "bars_factor", SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: subject, Freq: "1m", StartTime: start, EndTime: end, LookbackPeriods: 20,
		Factor: engine.FactorSpec{FactorID: "bias", Name: "Bias", Outputs: []string{"bias"}},
	}, TriggerType: "view_ready"}
}

type fakeStorage struct {
	mu           sync.Mutex
	chunks       []*storageio.RangeChunk
	writeSizes   []int
	writePeriods []int64
	writeErr     error
	repeatFirst  bool
}

type timeoutReadStorage struct {
	mu    sync.Mutex
	calls int
}

func (s *timeoutReadStorage) ReadRangeChunk(ctx context.Context, _ storageio.WindowKey, _ time.Time, _ time.Time, _ int, _ int, _ []string) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*timeoutReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	return 0, nil
}

func (s *timeoutReadStorage) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *fakeStorage) ReadRangeChunk(
	_ context.Context,
	_ storageio.WindowKey,
	_ time.Time,
	_ time.Time,
	_ int,
	_ int,
	_ []string,
) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chunks) == 0 {
		return &storageio.RangeChunk{Frame: &engine.DataFrame{}, Complete: true}, nil
	}
	chunk := s.chunks[0]
	if !s.repeatFirst {
		s.chunks = s.chunks[1:]
	}
	return chunk, nil
}

func (s *fakeStorage) WriteFactorPatch(_ context.Context, task *engine.FactorTask, result *engine.FactorResult) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writePeriods = append(s.writePeriods, task.PeriodTime)
	s.writeSizes = append(s.writeSizes, len(result.Rows))
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return uint64(len(result.Rows)), nil
}

type fakeExecutor struct {
	mu                sync.Mutex
	calls             int
	err               error
	transientFailures int
}

func (e *fakeExecutor) Execute(_ context.Context, _ *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.transientFailures > 0 {
		e.transientFailures--
		return nil, errors.New("transient worker failure")
	}
	if e.err != nil {
		return nil, e.err
	}
	rows := make([]engine.FactorResultRow, 0, len(frame.DataTimes))
	for index, dataTime := range frame.DataTimes {
		seriesTag := ""
		if index < len(frame.SeriesTags) {
			seriesTag = frame.SeriesTags[index]
		}
		rows = append(rows, engine.FactorResultRow{DataTime: dataTime, SeriesTag: seriesTag, Values: map[string]any{"bias": nil}})
	}
	return &engine.FactorResult{Rows: rows}, nil
}

func (e *fakeExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (*fakeExecutor) Close() error { return nil }

func makeTimes(start time.Time, count int) []time.Time {
	out := make([]time.Time, count)
	for index := range out {
		out[index] = start.Add(time.Duration(index) * time.Minute)
	}
	return out
}

func frameChunk(times []time.Time) *storageio.RangeChunk {
	return &storageio.RangeChunk{
		Frame:         &engine.DataFrame{DataTimes: times, SeriesTags: make([]string, len(times))},
		TargetPeriods: times, Complete: true,
	}
}
