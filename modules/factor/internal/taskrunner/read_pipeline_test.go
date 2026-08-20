package taskrunner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestBuildPeriodReadGroupsSharesSubjectWindowAndKeepsTriggerIdentity(t *testing.T) {
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	bias := oneBarTask("BTC-USDT", base)
	bias.PeriodTime = base.Unix()
	bias.TriggerType = "view_ready"
	bias.TriggerEventID = "ready-1"
	bias.LookbackPeriods = 20
	bias.Factor.InputColumns = []string{"close"}
	cci := bias
	cci.Factor.FactorID = "cci"
	cci.Factor.InputColumns = []string{"high", "low", "close"}
	longer := bias
	longer.Factor.FactorID = "bias-60"
	longer.LookbackPeriods = 60
	differentTrigger := bias
	differentTrigger.Factor.FactorID = "other-trigger"
	differentTrigger.TriggerEventID = "ready-2"
	rangeTask := bias
	rangeTask.Factor.FactorID = "range"
	rangeTask.PeriodTime = 0

	groups, singles := buildPeriodReadGroups([]Task{bias, cci, longer, differentTrigger, rangeTask})

	require.Len(t, groups, 2)
	require.Len(t, singles, 1)
	require.Equal(t, "range", singles[0].task.Factor.FactorID)
	var shared *periodReadGroup
	for _, group := range groups {
		if len(group.members) == 3 {
			shared = group
		}
	}
	require.NotNil(t, shared)
	require.Equal(t, []string{"close", "high", "low"}, shared.columns)
	require.Equal(t, 60, shared.lookbackPeriods)
	require.Equal(t, []int{0, 1, 2}, []int{shared.members[0].index, shared.members[1].index, shared.members[2].index})
}

func TestPreparedTaskProjectsAtComputeTime(t *testing.T) {
	shared := &storageio.RangeChunk{Frame: &engine.DataFrame{
		Columns: []string{"close", "high", "low"},
		Rows:    [][]any{{100.0, 105.0, 95.0}},
	}}
	prepared := preparedTask{
		task:   Task{FactorTask: engine.FactorTask{Factor: engine.FactorSpec{InputColumns: []string{"close", "low"}}}},
		shared: shared,
	}

	projected, err := prepared.project()

	require.NoError(t, err)
	require.Equal(t, []string{"close", "low"}, projected.Frame.Columns)
	require.Equal(t, [][]any{{100.0, 95.0}}, projected.Frame.Rows)
	require.Same(t, shared, prepared.shared)
}

type pipelineReadStorage struct {
	mu          sync.Mutex
	started     []string
	attempts    map[string]int
	active      int
	maxActive   int
	block       map[string]chan struct{}
	timeoutFor  map[string]bool
	nonRetryFor map[string]bool
}

func newPipelineReadStorage() *pipelineReadStorage {
	return &pipelineReadStorage{
		attempts: make(map[string]int), block: make(map[string]chan struct{}),
		timeoutFor: make(map[string]bool), nonRetryFor: make(map[string]bool),
	}
}

func (s *pipelineReadStorage) ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error) {
	return nil, errors.New("unexpected range read")
}

func (s *pipelineReadStorage) ReadPeriodChunk(ctx context.Context, key storageio.WindowKey, start, _ time.Time, _ int, columns []string) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	s.started = append(s.started, key.SubjectID)
	s.attempts[key.SubjectID]++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	block := s.block[key.SubjectID]
	timeout := s.timeoutFor[key.SubjectID]
	nonRetry := s.nonRetryFor[key.SubjectID]
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if nonRetry {
		return nil, engine.NonRetryableError{Err: errors.New("invalid projection")}
	}
	if timeout {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	row := make([]any, len(columns))
	for i := range row {
		row[i] = float64(i + 1)
	}
	return &storageio.RangeChunk{
		Frame:         &engine.DataFrame{Columns: append([]string(nil), columns...), Rows: [][]any{row}, DataTimes: []time.Time{start}},
		TargetPeriods: []time.Time{start}, Complete: true,
	}, nil
}

func (s *pipelineReadStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error) {
	return 1, nil
}

func (s *pipelineReadStorage) snapshot() (started []string, maxActive int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.started...), s.maxActive
}

type subjectBlockingExecutor struct {
	entered chan string
	release chan struct{}
}

func (e *subjectBlockingExecutor) Execute(ctx context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	e.entered <- task.SubjectID
	if task.SubjectID == "BTC" {
		select {
		case <-e.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return resultForFrame(frame), nil
}

func (*subjectBlockingExecutor) Close() error { return nil }

func TestRunAllOverlapsNextSubjectReadWithPythonExecution(t *testing.T) {
	storage := newPipelineReadStorage()
	exec := &subjectBlockingExecutor{entered: make(chan string, 4), release: make(chan struct{})}
	runner := NewService(2, storage, exec, WithViewReadConfig(1, time.Second))
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	var tasks []Task
	for _, subject := range []string{"BTC", "ETH"} {
		for _, factorID := range []string{"bias-5", "bias-20"} {
			task := oneBarTask(subject, base)
			task.PeriodTime = base.Unix()
			task.TriggerEventID = "ready-1"
			task.Factor.FactorID = factorID
			tasks = append(tasks, task)
		}
	}
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), tasks) }()

	firstSubject := <-exec.entered
	require.Contains(t, []string{"BTC", "ETH"}, firstSubject)
	require.Eventually(t, func() bool {
		started, _ := storage.snapshot()
		return len(started) >= 2 && started[1] == "ETH"
	}, time.Second, time.Millisecond)
	close(exec.release)
	for _, result := range <-done {
		require.NoError(t, result.Err)
	}
}

func TestRunAllRefillsReadWindowBeforeEarlierSlowReadFinishes(t *testing.T) {
	storage := newPipelineReadStorage()
	storage.block["A"] = make(chan struct{})
	runner := NewService(1, storage, &fakeExecutor{}, WithViewReadConfig(2, time.Second))
	base := time.Date(2026, 8, 10, 7, 5, 0, 0, time.UTC)
	tasks := make([]Task, 0, 3)
	for _, subject := range []string{"A", "B", "C"} {
		task := oneBarTask(subject, base)
		task.PeriodTime = base.Unix()
		task.TriggerEventID = "ready-refill"
		tasks = append(tasks, task)
	}
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), tasks) }()

	require.Eventually(t, func() bool {
		started, _ := storage.snapshot()
		return len(started) == 3
	}, time.Second, time.Millisecond, "C must refill B's read slot while A is blocked")
	started, _ := storage.snapshot()
	require.ElementsMatch(t, []string{"A", "B"}, started[:2])
	require.Equal(t, "C", started[2])
	close(storage.block["A"])
	for _, result := range <-done {
		require.NoError(t, result.Err)
	}
}

func TestRunAllReadTimeoutMovesGroupToTail(t *testing.T) {
	storage := newPipelineReadStorage()
	storage.timeoutFor["SLOW"] = true
	runner := NewService(1, storage, &fakeExecutor{}, WithViewReadConfig(1, 20*time.Millisecond))
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	tasks := make([]Task, 0, 3)
	for _, subject := range []string{"SLOW", "FAST-1", "FAST-2"} {
		task := oneBarTask(subject, base)
		task.PeriodTime = base.Unix()
		task.TriggerEventID = "ready-1"
		tasks = append(tasks, task)
	}

	results := runner.RunAll(context.Background(), tasks)

	started, maxActive := storage.snapshot()
	require.Equal(t, []string{"SLOW", "FAST-1", "FAST-2", "SLOW"}, started)
	require.Equal(t, 1, maxActive)
	require.ErrorIs(t, results[0].Err, context.DeadlineExceeded)
	require.NoError(t, results[1].Err)
	require.NoError(t, results[2].Err)
}

func TestRunAllNonRetryableReadErrorDoesNotRetry(t *testing.T) {
	storage := newPipelineReadStorage()
	storage.nonRetryFor["BAD"] = true
	runner := NewService(1, storage, &fakeExecutor{}, WithViewReadConfig(1, time.Second))
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	task := oneBarTask("BAD", base)
	task.PeriodTime = base.Unix()
	task.TriggerEventID = "ready-1"

	results := runner.RunAll(context.Background(), []Task{task})

	started, _ := storage.snapshot()
	require.Equal(t, []string{"BAD"}, started)
	require.ErrorContains(t, results[0].Err, "invalid projection")
}

func TestRunAllCancellationStopsPendingPeriodReads(t *testing.T) {
	storage := newPipelineReadStorage()
	storage.timeoutFor["A"] = true
	runner := NewService(2, storage, &fakeExecutor{}, WithViewReadConfig(1, time.Second))
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	var tasks []Task
	for _, subject := range []string{"A", "B", "C"} {
		task := oneBarTask(subject, base)
		task.PeriodTime = base.Unix()
		task.TriggerEventID = "ready-1"
		tasks = append(tasks, task)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(ctx, tasks) }()
	require.Eventually(t, func() bool {
		started, _ := storage.snapshot()
		return len(started) == 1
	}, time.Second, time.Millisecond)
	cancel()

	results := <-done
	started, _ := storage.snapshot()
	require.Equal(t, []string{"A"}, started)
	for _, result := range results {
		require.ErrorIs(t, result.Err, context.Canceled)
	}
	require.Equal(t, Status{Workers: 2}, runner.Status())
}

func TestPeriodViewReadDoesNotCountAsActivePythonTask(t *testing.T) {
	storage := newPipelineReadStorage()
	storage.block["BTC"] = make(chan struct{})
	runner := NewService(2, storage, &fakeExecutor{}, WithViewReadConfig(1, time.Second))
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	task := oneBarTask("BTC", base)
	task.PeriodTime = base.Unix()
	task.TriggerEventID = "ready-1"
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), []Task{task}) }()
	require.Eventually(t, func() bool {
		started, _ := storage.snapshot()
		return len(started) == 1
	}, time.Second, time.Millisecond)

	require.Equal(t, Status{Workers: 2, PendingTasks: 1}, runner.Status())
	close(storage.block["BTC"])
	require.NoError(t, (<-done)[0].Err)
	require.Equal(t, Status{Workers: 2}, runner.Status())
}

func TestViewReadConcurrencyIsIndependentFromPythonWorkers(t *testing.T) {
	storage := newPipelineReadStorage()
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	var tasks []Task
	for _, subject := range []string{"A", "B", "C", "D", "E"} {
		storage.block[subject] = make(chan struct{})
		task := oneBarTask(subject, base)
		task.PeriodTime = base.Unix()
		task.TriggerEventID = "ready-1"
		tasks = append(tasks, task)
	}
	runner := NewService(1, storage, &fakeExecutor{}, WithViewReadConfig(4, time.Second))
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), tasks) }()
	require.Eventually(t, func() bool {
		started, maxActive := storage.snapshot()
		return len(started) == 4 && maxActive == 4
	}, time.Second, time.Millisecond)

	started, maxActive := storage.snapshot()
	require.Len(t, started, 4)
	require.Equal(t, 4, maxActive)
	for _, release := range storage.block {
		close(release)
	}
	for _, result := range <-done {
		require.NoError(t, result.Err)
	}
}

func TestPreparedQueueBackpressureStopsAdditionalViewReads(t *testing.T) {
	storage := newPipelineReadStorage()
	exec := &blockingExecutor{entered: make(chan string, 8), release: make(chan struct{})}
	runner := NewService(1, storage, exec, WithViewReadConfig(4, time.Second))
	base := time.Date(2026, 8, 10, 7, 4, 0, 0, time.UTC)
	var tasks []Task
	for _, subject := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		task := oneBarTask(subject, base)
		task.PeriodTime = base.Unix()
		task.TriggerEventID = "ready-1"
		tasks = append(tasks, task)
	}
	done := make(chan []Result, 1)
	go func() { done <- runner.RunAll(context.Background(), tasks) }()
	<-exec.entered
	time.Sleep(50 * time.Millisecond)
	started, _ := storage.snapshot()
	require.LessOrEqual(t, len(started), 7, "reads are bounded by read slots, the prepared queue, and the active task")
	require.NotContains(t, started, "H", "the final subject must remain unread while downstream is backpressured")

	close(exec.release)
	for _, result := range <-done {
		require.NoError(t, result.Err)
	}
}
