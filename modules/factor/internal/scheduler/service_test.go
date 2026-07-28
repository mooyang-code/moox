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
	specs := []engine.FactorSpec{{Name: "Cci", Periods: []int{20}}}
	require.NoError(t, validateFactorResult(specs, 2, &engine.FactorResult{
		Columns: map[string][]any{"Cci_20": {nil, 1.25}},
	}))
	for _, value := range []any{"bad", math.NaN(), math.Inf(1)} {
		require.Error(t, validateFactorResult(specs, 1, &engine.FactorResult{
			Columns: map[string][]any{"Cci_20": {value}},
		}))
	}
	require.Error(t, validateFactorResult(specs, 2, &engine.FactorResult{
		Columns: map[string][]any{"Cci_20": {1.0}},
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

func TestRunFailsWithoutTargetRows(t *testing.T) {
	svc := NewService(Config{}, &fakeStorage{chunks: []*storageio.RangeChunk{{Frame: &engine.DataFrame{}}}}, &fakeExecutor{})
	require.Error(t, svc.Run(context.Background(), oneBarTask("BTC", time.Unix(1, 0))))
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
	return Task{FactorTask: engine.FactorTask{
		TaskID: "task-" + subject + at.String(), SpaceID: "crypto",
		SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: subject, Freq: "1m", StartTime: at, EndTime: at.Add(time.Nanosecond),
		LookbackBars: 20, Factors: []engine.FactorSpec{{FactorID: "bias", Name: "Bias", Periods: []int{20}}},
	}, TriggerType: "event"}
}

type fakeStorage struct {
	chunks      []*storageio.RangeChunk
	writeSizes  []int
	writeErr    error
	repeatFirst bool
}

func (s *fakeStorage) ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error) {
	if len(s.chunks) == 0 {
		return &storageio.RangeChunk{Frame: &engine.DataFrame{}}, nil
	}
	chunk := s.chunks[0]
	if !s.repeatFirst {
		s.chunks = s.chunks[1:]
	}
	return chunk, nil
}
func (s *fakeStorage) WriteFactorPatch(_ context.Context, _ *engine.FactorTask, times []time.Time, _ *engine.FactorResult) (uint64, error) {
	s.writeSizes = append(s.writeSizes, len(times))
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return uint64(len(times)), nil
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
	targetRows := 0
	for range frame.DataTimes {
		targetRows++
	}
	return &engine.FactorResult{Columns: map[string][]any{"Bias_20": make([]any, targetRows)}}, nil
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
	return &storageio.RangeChunk{Frame: &engine.DataFrame{DataTimes: times}, TargetTimes: times}
}
