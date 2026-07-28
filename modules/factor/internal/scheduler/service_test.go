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

func TestSchedulerSupersedeMergesPendingRanges(t *testing.T) {
	svc := NewService(Config{Workers: 1, QueueCapacity: 1}, nil, nil)
	first := rangeTask("BTC", time.Unix(10, 0), time.Unix(20, 0))
	first.TaskID = "first"
	first.LookbackRows = 2
	first.Factors = []engine.FactorSpec{{FactorID: "old", Outputs: []string{"old"}}}
	second := rangeTask("BTC", time.Unix(5, 0), time.Unix(15, 0))
	second.TaskID = "second"
	second.LookbackRows = 7
	second.Factors = []engine.FactorSpec{{FactorID: "new", Outputs: []string{"new"}}}
	third := rangeTask("BTC", time.Unix(12, 0), time.Unix(30, 0))
	third.TaskID = "third"
	third.LookbackRows = 11
	third.Factors = []engine.FactorSpec{{FactorID: "latest", Outputs: []string{"latest"}}}

	require.NoError(t, svc.Enqueue(context.Background(), first))
	require.NoError(t, svc.Enqueue(context.Background(), second))
	require.NoError(t, svc.Enqueue(context.Background(), third))
	queued, ok := svc.popShard(0, false)
	require.True(t, ok)
	require.Equal(t, time.Unix(5, 0), queued.StartTime)
	require.Equal(t, time.Unix(30, 0), queued.EndTime)
	require.Equal(t, "third", queued.TaskID)
	require.Equal(t, 11, queued.LookbackRows)
	require.Equal(t, third.Factors, queued.Factors)
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
	specs := []engine.FactorSpec{{Name: "Cci", Outputs: []string{"cci"}}}
	require.NoError(t, validateFactorResult(specs, 2, &engine.FactorResult{
		Columns: map[string][]any{"cci": {nil, 1.25}},
	}))
	for _, value := range []any{"bad", math.NaN(), math.Inf(1)} {
		require.Error(t, validateFactorResult(specs, 1, &engine.FactorResult{
			Columns: map[string][]any{"cci": {value}},
		}))
	}
	require.Error(t, validateFactorResult(specs, 2, &engine.FactorResult{
		Columns: map[string][]any{"cci": {1.0}},
	}))
	require.Error(t, validateFactorResult(specs, 1, &engine.FactorResult{
		Columns: map[string][]any{"extra": {1.0}},
	}))
	require.Error(t, validateFactorResult(
		[]engine.FactorSpec{{Outputs: []string{"shared"}}, {Outputs: []string{"shared"}}},
		1,
		&engine.FactorResult{Columns: map[string][]any{"shared": {1.0}}},
	))
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

func oneBarTask(subject string, at time.Time) Task {
	return rangeTask(subject, at, at.Add(time.Nanosecond))
}

func rangeTask(subject string, start, end time.Time) Task {
	return Task{FactorTask: engine.FactorTask{
		TaskID: "task-" + subject, SpaceID: "crypto",
		SourceDataset: "bars", TargetDataset: "bars_factor",
		SubjectID: subject, Freq: "1m", StartTime: start, EndTime: end,
		LookbackRows: 20, Factors: []engine.FactorSpec{{FactorID: "bias", Name: "Bias", Outputs: []string{"bias"}}},
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
func (s *fakeStorage) WriteFactorPatch(_ context.Context, _ *engine.FactorTask, times []time.Time, _ *engine.FactorResult) error {
	s.writeSizes = append(s.writeSizes, len(times))
	return s.writeErr
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
	return &engine.FactorResult{Columns: map[string][]any{"bias": make([]any, targetRows)}}, nil
}

func TestInputColumnsUsesOnlyDeclaredColumns(t *testing.T) {
	specs := []engine.FactorSpec{
		{InputColumns: []string{"nav", "benchmark_return"}},
		{InputColumns: []string{"nav", "risk_free_rate"}},
	}
	require.Equal(t, []string{"benchmark_return", "nav", "risk_free_rate"}, inputColumns(specs))
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
