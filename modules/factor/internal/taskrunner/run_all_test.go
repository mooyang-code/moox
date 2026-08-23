package taskrunner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

type blockingReadStorage struct {
	active  atomic.Int64
	maximum atomic.Int64
	entered chan struct{}
	release chan struct{}
}

func (s *blockingReadStorage) ReadRangeChunk(
	ctx context.Context,
	_ storageio.WindowKey,
	start time.Time,
	_ time.Time,
	_ int,
	_ int,
	_ []string,
) (*storageio.RangeChunk, error) {
	current := s.active.Add(1)
	defer s.active.Add(-1)
	observeMaximum(&s.maximum, current)
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return frameChunk([]time.Time{start}), nil
}

func (*blockingReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	return 1, nil
}

func observeMaximum(maximum *atomic.Int64, current int64) {
	for {
		seen := maximum.Load()
		if current <= seen || maximum.CompareAndSwap(seen, current) {
			return
		}
	}
}

func TestRunAllBoundsWholeTaskConcurrency(t *testing.T) {
	storage := &blockingReadStorage{
		entered: make(chan struct{}, 7),
		release: make(chan struct{}),
	}
	runner := NewService(3, storage, &fakeExecutor{})
	base := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	tasks := make([]Task, 7)
	for index := range tasks {
		tasks[index] = oneBarTask(fmt.Sprintf("S%d", index), base)
	}
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), tasks) }()

	for range 3 {
		<-storage.entered
	}
	select {
	case <-storage.entered:
		t.Fatal("fourth task read View before a worker slot was released")
	case <-time.After(50 * time.Millisecond):
	}
	require.EqualValues(t, 3, storage.maximum.Load())
	require.Equal(t, Status{Workers: 3, ActiveTasks: 3, PendingTasks: 4}, runner.Status())

	close(storage.release)
	results := <-done
	require.Len(t, results, 7)
	for _, result := range results {
		require.NoError(t, result.Err)
	}
	require.Equal(t, Status{Workers: 3}, runner.Status())
}

func TestRunAllExecutesSubjectFactorCartesianProduct(t *testing.T) {
	exec := &recordingExecutor{}
	runner := NewService(4, staticReadStorage{}, exec)
	base := time.Unix(1, 0).UTC()
	var tasks []Task
	for _, subject := range []string{"BTC", "ETH", "SOL"} {
		for _, factor := range []string{"bias5", "bias20"} {
			task := oneBarTask(subject, base)
			task.Factor.FactorID = factor
			tasks = append(tasks, task)
		}
	}

	results := runner.RunAll(context.Background(), tasks)
	require.Len(t, results, 6)
	for _, result := range results {
		require.NoError(t, result.Err)
	}
	require.ElementsMatch(t, []string{
		"BTC/bias5", "BTC/bias20", "ETH/bias5", "ETH/bias20", "SOL/bias5", "SOL/bias20",
	}, exec.executed())
}

func TestRunAllUsesBatchFactorWriterForOneSubjectPeriod(t *testing.T) {
	storage := &batchRecordingStorage{}
	runner := NewService(2, storage, &recordingBatchExecutor{})
	base := time.Unix(1, 0).UTC()
	first := oneBarTask("BTC", base)
	first.Factor.FactorID = "bias5"
	first.TaskID = "task-btc-bias5"
	first.PeriodTime = base.Unix()
	second := oneBarTask("BTC", base)
	second.Factor.FactorID = "bias20"
	second.TaskID = "task-btc-bias20"
	second.PeriodTime = base.Unix()
	results := runner.RunAll(context.Background(), []Task{first, second})
	for _, result := range results {
		require.NoError(t, result.Err)
	}
	require.Equal(t, 1, storage.batchCalls)
	require.Equal(t, 2, storage.batchSize)
	require.Zero(t, storage.singleCalls)
}

func TestRunAllAllowsSameSubjectFactorsToRunConcurrently(t *testing.T) {
	exec := &blockingExecutor{entered: make(chan string, 2), release: make(chan struct{})}
	runner := NewService(2, staticReadStorage{}, exec, WithBatchExecution(false))
	base := time.Unix(1, 0).UTC()
	first := oneBarTask("BTC", base)
	first.Factor.FactorID = "bias5"
	second := oneBarTask("BTC", base)
	second.Factor.FactorID = "bias20"
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), []Task{first, second}) }()

	require.ElementsMatch(t, []string{"bias5", "bias20"}, []string{<-exec.entered, <-exec.entered})
	close(exec.release)
	for _, result := range <-done {
		require.NoError(t, result.Err)
	}
}

func TestRunAllSharesPeriodReadAcrossSameSubjectFactors(t *testing.T) {
	storage := &recordingReadStorage{}
	exec := &frameRecordingExecutor{}
	runner := NewService(2, storage, exec)
	base := time.Date(2026, 8, 10, 6, 10, 0, 0, time.UTC)
	first := oneBarTask("BTC-USDT", base)
	first.PeriodTime = base.Unix()
	first.Factor.FactorID = "bias"
	first.Factor.InputColumns = []string{"close"}
	second := oneBarTask("BTC-USDT", base)
	second.PeriodTime = base.Unix()
	second.Factor.FactorID = "cci"
	second.Factor.InputColumns = []string{"close", "high", "low"}

	results := runner.RunAll(context.Background(), []Task{first, second})

	for _, result := range results {
		require.NoError(t, result.Err)
	}
	require.Equal(t, 0, storage.readCount())
	require.Equal(t, 1, storage.periodReadCount())
	require.ElementsMatch(t, []string{"close", "high", "low"}, storage.readColumns())
	require.Equal(t, []string{"close"}, exec.columnsFor("bias"))
	require.Equal(t, []string{"close", "high", "low"}, exec.columnsFor("cci"))
}

func TestRunAllBatchesFactorsBySubject(t *testing.T) {
	storage := &recordingReadStorage{}
	exec := &recordingBatchExecutor{}
	runner := NewService(2, storage, exec)
	base := time.Date(2026, 8, 10, 6, 10, 0, 0, time.UTC)
	first := oneBarTask("BTC-USDT", base)
	first.PeriodTime = base.Unix()
	first.Factor.FactorID = "bias"
	first.TaskID = "task-btc-bias"
	second := oneBarTask("BTC-USDT", base)
	second.PeriodTime = base.Unix()
	second.Factor.FactorID = "cci"
	second.TaskID = "task-btc-cci"
	second.LookbackPeriods = 600

	results := runner.RunAll(context.Background(), []Task{first, second})
	for _, result := range results {
		require.NoError(t, result.Err)
	}
	require.Equal(t, 1, exec.batchCalls)
	require.Equal(t, 2, exec.batchFactors)
	require.Equal(t, 1, storage.periodReadCount())
}

func TestRunAllIndividualFallbackPreservesMemberLookback(t *testing.T) {
	base := time.Date(2026, 8, 10, 6, 10, 0, 0, time.UTC)
	for _, batchEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch_enabled_%t", batchEnabled), func(t *testing.T) {
			storage := &lookbackReadStorage{}
			exec := &frameRecordingExecutor{}
			runner := NewService(2, storage, exec, WithBatchExecution(batchEnabled))
			short := oneBarTask("BTC-USDT", base)
			short.PeriodTime = base.Unix()
			short.TaskID = "task-short"
			short.Factor.FactorID = "short"
			short.LookbackPeriods = 2
			long := oneBarTask("BTC-USDT", base)
			long.PeriodTime = base.Unix()
			long.TaskID = "task-long"
			long.Factor.FactorID = "long"
			long.LookbackPeriods = 4

			results := runner.RunAll(context.Background(), []Task{short, long})
			for _, result := range results {
				require.NoError(t, result.Err)
			}
			require.Equal(t, 2, exec.rowsFor("short"))
			require.Equal(t, 4, exec.rowsFor("long"))
		})
	}
}

func TestRunAllFailureDoesNotBlockOtherTasks(t *testing.T) {
	exec := &selectiveFailureExecutor{failedFactor: "bad"}
	runner := NewService(2, staticReadStorage{}, exec)
	base := time.Unix(1, 0).UTC()
	tasks := []Task{oneBarTask("BTC", base), oneBarTask("ETH", base), oneBarTask("SOL", base)}
	tasks[0].Factor.FactorID = "bad"
	tasks[1].Factor.FactorID = "good-1"
	tasks[2].Factor.FactorID = "good-2"

	results := runner.RunAll(context.Background(), tasks)
	require.Error(t, results[0].Err)
	require.NoError(t, results[1].Err)
	require.NoError(t, results[2].Err)
	require.ElementsMatch(t, []string{"bad", "bad", "good-1", "good-2"}, exec.executed())
	require.Equal(t, Status{Workers: 2}, runner.Status())
}

func TestRunAllCancellationTerminatesStartedAndPendingTasks(t *testing.T) {
	storage := &blockingReadStorage{entered: make(chan struct{}, 2), release: make(chan struct{})}
	runner := NewService(2, storage, &fakeExecutor{})
	base := time.Unix(1, 0).UTC()
	tasks := make([]Task, 20)
	for index := range tasks {
		tasks[index] = oneBarTask(fmt.Sprintf("S%d", index), base)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(ctx, tasks) }()
	<-storage.entered
	<-storage.entered
	cancel()

	results := <-done
	for _, result := range results {
		require.ErrorIs(t, result.Err, context.Canceled)
	}
	require.Equal(t, Status{Workers: 2}, runner.Status())
}

type staticReadStorage struct{}

func (staticReadStorage) ReadRangeChunk(
	_ context.Context,
	_ storageio.WindowKey,
	start time.Time,
	_ time.Time,
	_ int,
	_ int,
	_ []string,
) (*storageio.RangeChunk, error) {
	return frameChunk([]time.Time{start}), nil
}

func (staticReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	return 1, nil
}

type batchRecordingStorage struct {
	staticReadStorage
	batchCalls  int
	batchSize   int
	singleCalls int
}

func (s *batchRecordingStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	s.singleCalls++
	return 1, nil
}

func (s *batchRecordingStorage) WriteFactorPatches(_ context.Context, patches []storageio.FactorPatch) ([]uint64, error) {
	s.batchCalls++
	s.batchSize = len(patches)
	counts := make([]uint64, len(patches))
	for index := range counts {
		counts[index] = 1
	}
	return counts, nil
}

type recordingReadStorage struct {
	mu          sync.Mutex
	reads       int
	periodReads int
	columns     []string
}

type lookbackReadStorage struct{}

func (lookbackReadStorage) ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error) {
	return nil, errors.New("period read expected")
}

func (lookbackReadStorage) ReadPeriodChunk(_ context.Context, _ storageio.WindowKey, start, _ time.Time, _ int, columns []string) (*storageio.RangeChunk, error) {
	times := []time.Time{start.Add(-3 * time.Minute), start.Add(-2 * time.Minute), start.Add(-time.Minute), start}
	rows := make([][]any, len(times))
	for index := range rows {
		rows[index] = make([]any, len(columns))
	}
	return &storageio.RangeChunk{
		Frame:         &engine.DataFrame{Columns: append([]string(nil), columns...), Rows: rows, DataTimes: times, SeriesTags: []string{"venue:binance", "venue:binance", "venue:binance", "venue:binance"}},
		TargetPeriods: []time.Time{start}, Complete: true,
	}, nil
}

func (lookbackReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	return 1, nil
}

func (s *recordingReadStorage) ReadRangeChunk(
	_ context.Context,
	_ storageio.WindowKey,
	start time.Time,
	_ time.Time,
	_ int,
	_ int,
	columns []string,
) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	s.reads++
	s.columns = append([]string(nil), columns...)
	s.mu.Unlock()
	return recordingRangeChunk(start, columns), nil
}

func (s *recordingReadStorage) ReadPeriodChunk(
	_ context.Context,
	_ storageio.WindowKey,
	start time.Time,
	_ time.Time,
	_ int,
	columns []string,
) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	s.periodReads++
	s.columns = append([]string(nil), columns...)
	s.mu.Unlock()
	return recordingRangeChunk(start, columns), nil
}

func recordingRangeChunk(start time.Time, columns []string) *storageio.RangeChunk {
	row := make([]any, len(columns))
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			Columns: columns, Rows: [][]any{row}, DataTimes: []time.Time{start}, SeriesTags: []string{"venue:binance"},
		},
		TargetPeriods: []time.Time{start}, Complete: true,
	}
}

func (*recordingReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	return 1, nil
}

func (s *recordingReadStorage) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

func (s *recordingReadStorage) periodReadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.periodReads
}

func (s *recordingReadStorage) readColumns() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.columns...)
}

type frameRecordingExecutor struct {
	mu      sync.Mutex
	columns map[string][]string
	rows    map[string]int
}

func (e *frameRecordingExecutor) Execute(_ context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.mu.Lock()
	if e.columns == nil {
		e.columns = make(map[string][]string)
	}
	if e.rows == nil {
		e.rows = make(map[string]int)
	}
	e.columns[task.Factor.FactorID] = append([]string(nil), frame.Columns...)
	e.rows[task.Factor.FactorID] = len(frame.Rows)
	e.mu.Unlock()
	return resultForFrame(frame), nil
}

func (*frameRecordingExecutor) Close() error { return nil }

func (e *frameRecordingExecutor) columnsFor(factorID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.columns[factorID]...)
}

func (e *frameRecordingExecutor) rowsFor(factorID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rows[factorID]
}

type recordingExecutor struct {
	mu    sync.Mutex
	calls []string
}

type recordingBatchExecutor struct {
	batchCalls   int
	batchFactors int
}

func (e *recordingBatchExecutor) Execute(context.Context, *engine.FactorTask, *engine.DataFrame) (*engine.FactorResult, error) {
	return nil, errors.New("single execution should not be used")
}

func (e *recordingBatchExecutor) ExecuteBatch(_ context.Context, batch *engine.BatchTask, frame *engine.DataFrame) (*engine.BatchResult, error) {
	e.batchCalls++
	e.batchFactors += len(batch.Tasks)
	items := make([]engine.BatchItemResult, 0, len(batch.Tasks))
	for _, task := range batch.Tasks {
		items = append(items, engine.BatchItemResult{TaskID: task.TaskID, BindingID: task.BindingID, Result: resultForFrame(frame)})
	}
	return &engine.BatchResult{BatchID: batch.BatchID, Items: items}, nil
}

func (*recordingBatchExecutor) Close() error { return nil }

func (e *recordingExecutor) Execute(_ context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, task.SubjectID+"/"+task.Factor.FactorID)
	e.mu.Unlock()
	return resultForFrame(frame), nil
}

func (e *recordingExecutor) executed() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (*recordingExecutor) Close() error { return nil }

type blockingExecutor struct {
	entered chan string
	release chan struct{}
}

func (e *blockingExecutor) Execute(ctx context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.entered <- task.Factor.FactorID
	select {
	case <-e.release:
		return resultForFrame(frame), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingExecutor) Close() error { return nil }

type selectiveFailureExecutor struct {
	mu           sync.Mutex
	failedFactor string
	calls        []string
}

func (e *selectiveFailureExecutor) Execute(_ context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, task.Factor.FactorID)
	e.mu.Unlock()
	if task.Factor.FactorID == e.failedFactor {
		return nil, errors.New("planned failure")
	}
	return resultForFrame(frame), nil
}

func (e *selectiveFailureExecutor) executed() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (*selectiveFailureExecutor) Close() error { return nil }

func resultForFrame(frame *engine.DataFrame) *engine.FactorResult {
	rows := make([]engine.FactorResultRow, len(frame.DataTimes))
	for index, dataTime := range frame.DataTimes {
		rows[index] = engine.FactorResultRow{DataTime: dataTime, Values: map[string]any{"bias": nil}}
	}
	return &engine.FactorResult{Rows: rows}
}
