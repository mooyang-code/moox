package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
)

// Config controls scheduler execution.
type Config struct {
	Workers             int
	MaxRetry            int
	BatchMinEstimatedMS int64
	SnapshotDir         string
}

// StorageIO is the storage dependency used by the scheduler.
type StorageIO interface {
	ReadWindow(ctx context.Context, key storageio.WindowKey, lookbackBars int, endTime time.Time, columns []string) (*engine.DataFrame, error)
	WriteFactorPatch(ctx context.Context, task *engine.FactorTask, frame *engine.DataFrame, result *engine.FactorResult) error
}

// Status is a lightweight runtime snapshot for RPC/management pages.
type Status struct {
	QueueDepth        int
	SupersedeCount    int64
	WritebackFailures int64
}

// Service owns task queues and execution.
type Service struct {
	cfg               Config
	storage           StorageIO
	exec              engine.Executor
	drainMu           sync.Mutex
	mu                sync.Mutex
	queues            [][]queueItem
	pending           map[taskKey]Task
	acceptedTaskIDs   map[string]struct{}
	supersedeCount    atomic.Int64
	writebackFailures atomic.Int64
	snapshotStore     *storageio.SnapshotStore
	running           atomic.Int64
	started           atomic.Bool
	workerCtx         context.Context
	workerCancel      context.CancelFunc
	workerWG          sync.WaitGroup
	wake              []chan struct{}
}

var logRun = func(ctx context.Context, line string) {
	log.InfoContextf(ctx, "%s", line)
}

// NewService creates a scheduler.
func NewService(cfg Config, storage StorageIO, exec engine.Executor) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.MaxRetry <= 0 {
		cfg.MaxRetry = 3
	}
	svc := &Service{
		cfg:     cfg,
		storage: storage,
		exec:    exec,
		queues:  make([][]queueItem, cfg.Workers),
		pending: map[taskKey]Task{},
		acceptedTaskIDs: map[string]struct{}{},
		wake:    make([]chan struct{}, cfg.Workers),
	}
	if cfg.SnapshotDir != "" {
		svc.snapshotStore = storageio.NewSnapshotStore(cfg.SnapshotDir)
	}
	for i := range svc.wake {
		svc.wake[i] = make(chan struct{}, 1)
	}
	return svc
}

// Enqueue adds a task, replacing older pending work for the same subject scope.
// It is kept as a compatibility wrapper for RPC/runtime callers that use the
// fire-and-forget scheduler interface.
func (s *Service) Enqueue(ctx context.Context, task Task) {
	_ = s.EnqueueChecked(ctx, task)
}

// EnqueueChecked is the durable trigger boundary: callers must not commit an
// event inbox until this method confirms that the task was accepted.
func (s *Service) EnqueueChecked(ctx context.Context, task Task) error {
	if s == nil {
		return errors.New("scheduler is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Event-triggered Factor tasks use a stable TaskID. Remember accepted IDs
	// so a failed inbox commit can be retried without re-enqueueing the same
	// logical task in this scheduler process.
	if task.TriggerType == "event" && task.TaskID != "" {
		if _, ok := s.acceptedTaskIDs[task.TaskID]; ok {
			return nil
		}
	}
	key := keyOf(task)
	if old, ok := s.pending[key]; ok {
		if task.BarTime.After(old.BarTime) || task.BarTime.Equal(old.BarTime) {
			s.supersedeCount.Add(1)
			_ = s.record(ctx, old, domain.RunStatusSuperseded, "superseded by newer bar", 0, nil)
			s.pending[key] = task
			shard := HashSubject(task.SubjectID, s.cfg.Workers)
			s.queues[shard] = append(s.queues[shard], queueItem{task: task})
			signal := s.wake[shard]
			select {
			case signal <- struct{}{}:
			default:
			}
			return nil
		}
		s.supersedeCount.Add(1)
		_ = s.record(ctx, task, domain.RunStatusSuperseded, "older than pending task", 0, nil)
		return nil
	}
	if task.TriggerType == "event" && task.TaskID != "" {
		s.acceptedTaskIDs[task.TaskID] = struct{}{}
	}
	s.pending[key] = task
	shard := HashSubject(task.SubjectID, s.cfg.Workers)
	s.queues[shard] = append(s.queues[shard], queueItem{task: task})
	signal := s.wake[shard]
	select {
	case signal <- struct{}{}:
	default:
	}
	return nil
}

// Start launches one FIFO consumer for each subject shard. Drain remains the
// deterministic CLI/test path; production uses these consumers.
func (s *Service) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("scheduler is nil")
	}
	if s.started.Swap(true) {
		return nil
	}
	s.workerCtx, s.workerCancel = context.WithCancel(ctx)
	for shard := range s.queues {
		s.workerWG.Add(1)
		go s.runShard(shard)
	}
	return nil
}

func (s *Service) runShard(shard int) {
	defer s.workerWG.Done()
	for {
		select {
		case <-s.workerCtx.Done():
			return
		case <-s.wake[shard]:
		}
		for {
			item, ok := s.popShard(shard)
			if !ok {
				break
			}
			s.running.Add(1)
			_ = s.executeWithRetry(s.workerCtx, item.task)
			s.running.Add(-1)
		}
	}
}

func (s *Service) popShard(shard int) (queueItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if shard < 0 || shard >= len(s.queues) || len(s.queues[shard]) == 0 {
		return queueItem{}, false
	}
	item := s.queues[shard][0]
	s.queues[shard] = s.queues[shard][1:]
	key := keyOf(item.task)
	current, ok := s.pending[key]
	if !ok || current.TaskID != item.task.TaskID || !current.BarTime.Equal(item.task.BarTime) {
		return s.popShardLocked(shard)
	}
	delete(s.pending, key)
	return item, true
}

func (s *Service) popShardLocked(shard int) (queueItem, bool) {
	for len(s.queues[shard]) > 0 {
		item := s.queues[shard][0]
		s.queues[shard] = s.queues[shard][1:]
		key := keyOf(item.task)
		current, ok := s.pending[key]
		if ok && current.TaskID == item.task.TaskID && current.BarTime.Equal(item.task.BarTime) {
			delete(s.pending, key)
			return item, true
		}
	}
	return queueItem{}, false
}

func (s *Service) WaitIdle(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.Status().QueueDepth == 0 && s.running.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) Stop() error {
	if s == nil || !s.started.Load() {
		return nil
	}
	if s.workerCancel != nil {
		s.workerCancel()
	}
	s.workerWG.Wait()
	s.started.Store(false)
	return nil
}

// Drain synchronously drains all currently queued tasks. Production startup uses
// worker goroutines; tests and run-once use this deterministic path.
func (s *Service) Drain(ctx context.Context) error {
	if s.started.Load() {
		// A started scheduler owns queue consumption. Do not let timer/CLI
		// callers pop the same queue concurrently; wait for its workers instead.
		return s.WaitIdle(ctx)
	}
	if err := s.lockDrain(ctx); err != nil {
		return err
	}
	defer s.drainMu.Unlock()

	failed := 0
	var firstErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, ok := s.popNext()
		if !ok {
			if failed == 0 {
				return nil
			}
			if failed == 1 {
				return firstErr
			}
			return fmt.Errorf("%d factor task(s) failed: %w", failed, firstErr)
		}
		key := keyOf(item.task)
		s.mu.Lock()
		current, stillPending := s.pending[key]
		if !stillPending || current.TaskID != item.task.TaskID || !current.BarTime.Equal(item.task.BarTime) {
			s.mu.Unlock()
			continue
		}
		delete(s.pending, key)
		s.mu.Unlock()
		if err := s.executeWithRetry(ctx, item.task); err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (s *Service) lockDrain(ctx context.Context) error {
	if s.drainMu.TryLock() {
		return nil
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if s.drainMu.TryLock() {
				return nil
			}
		}
	}
}

func (s *Service) popNext() (queueItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for shard := range s.queues {
		if len(s.queues[shard]) == 0 {
			continue
		}
		item := s.queues[shard][0]
		s.queues[shard] = s.queues[shard][1:]
		return item, true
	}
	return queueItem{}, false
}

func (s *Service) executeWithRetry(ctx context.Context, task Task) error {
	if !task.BarTime.IsZero() {
		_ = report.ObserveModuleInputWatermark("factor", "calculate", "factor-calculation", task.BarTime)
	}
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetry; attempt++ {
		elapsed, err := s.executeOnce(ctx, task)
		if err == nil {
			_ = s.record(ctx, task, domain.RunStatusSucceeded, "", elapsed, nil)
			_ = report.ObserveModuleRun("factor", "calculate", "success", "factor-calculation", time.Now())
			if !task.BarTime.IsZero() {
				_ = report.ObserveModuleWatermark("factor", "calculate", "factor-calculation", task.BarTime)
			}
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}
	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	_ = s.record(ctx, task, domain.RunStatusFailed, errMsg, 0, lastErr)
	_ = report.ObserveModuleRun("factor", "calculate", "error", "factor-calculation", time.Now())
	if lastErr == nil {
		return fmt.Errorf("factor task %s failed", task.TaskID)
	}
	return fmt.Errorf("factor task %s failed: %w", task.TaskID, lastErr)
}

func (s *Service) executeOnce(ctx context.Context, task Task) (int64, error) {
	if s.storage == nil || s.exec == nil {
		return 0, fmt.Errorf("scheduler dependencies are required")
	}
	started := time.Now()
	frame, err := s.storage.ReadWindow(ctx, storageio.WindowKey{
		SpaceID:       task.SpaceID,
		SourceDataset: task.SourceDataset,
		SubjectID:     task.SubjectID,
		Freq:          task.Freq,
	}, task.LookbackBars, task.BarTime, inputColumns(task.Factors))
	if err != nil {
		return 0, engine.RetryableError{Err: err}
	}
	if s.snapshotStore != nil {
		handle, snapshotErr := s.snapshotStore.AcquireFrame(ctx, task.FactorTask, frame)
		if snapshotErr != nil {
			return 0, engine.NonRetryableError{Err: snapshotErr}
		}
		if handle != nil {
			defer handle.Release()
			task.SnapshotID, task.SnapshotHash, task.SnapshotPath = handle.ID, handle.Hash, handle.Path
		}
	}
	result, err := s.executeFactorBatches(ctx, task.FactorTask, frame)
	if err != nil {
		return 0, err
	}
	if err := s.storage.WriteFactorPatch(ctx, &task.FactorTask, frame, result); err != nil {
		s.writebackFailures.Add(1)
		return result.ElapsedMS, engine.RetryableError{Err: err}
	}
	if result.ElapsedMS > 0 {
		return result.ElapsedMS, nil
	}
	return time.Since(started).Milliseconds(), nil
}

func (s *Service) executeFactorBatches(ctx context.Context, task engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	cost := make(map[string]int64, len(task.Factors))
	for _, spec := range task.Factors {
		cost[spec.FactorID] = spec.EstimatedMS
		if cost[spec.FactorID] <= 0 {
			cost[spec.FactorID] = 1
		}
	}
	threshold := s.cfg.BatchMinEstimatedMS
	if threshold <= 0 {
		threshold = 50
	}
	batches := Partition(task.Factors, cost, s.cfg.Workers, threshold)
	if len(batches) == 1 {
		result, err := s.exec.Execute(ctx, &task, frame)
		if err != nil {
			return nil, err
		}
		if err := validateFactorResult(task.Factors, result); err != nil {
			return nil, err
		}
		return result, nil
	}
	type item struct {
		result *engine.FactorResult
		err    error
	}
	results := make(chan item, len(batches))
	var wg sync.WaitGroup
	for _, batch := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub := task
			sub.Factors = batch.Factors
			sub.TaskID = task.TaskID
			result, err := s.exec.Execute(ctx, &sub, frame)
			if err == nil {
				err = validateFactorResult(batch.Factors, result)
			}
			results <- item{result, err}
		}()
	}
	wg.Wait()
	close(results)
	aggregated := &engine.FactorResult{Columns: map[string]engine.FactorColumnResult{}, PerFactorMS: map[string]int64{}}
	for item := range results {
		if item.err != nil {
			return nil, item.err
		}
		if item.result == nil {
			return nil, errors.New("nil factor batch result")
		}
		for name, column := range item.result.Columns {
			if _, exists := aggregated.Columns[name]; exists {
				return nil, fmt.Errorf("duplicate factor output column %s", name)
			}
			aggregated.Columns[name] = column
		}
		for name, ms := range item.result.PerFactorMS {
			aggregated.PerFactorMS[name] = ms
		}
		if item.result.ElapsedMS > aggregated.ElapsedMS {
			aggregated.ElapsedMS = item.result.ElapsedMS
		}
	}
	return aggregated, nil
}

func validateFactorResult(specs []engine.FactorSpec, result *engine.FactorResult) error {
	if result == nil {
		return errors.New("nil factor result")
	}
	expected := make(map[string]struct{})
	for _, spec := range specs {
		for _, param := range spec.Params {
			expected[fmt.Sprintf("%s_%d", spec.Name, param)] = struct{}{}
		}
	}
	if len(expected) == 0 {
		return nil
	}
	if len(result.Columns) != len(expected) {
		return fmt.Errorf("factor result columns=%d expected=%d", len(result.Columns), len(expected))
	}
	for name, col := range result.Columns {
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("unexpected factor output column %s", name)
		}
		if col.Tail <= 0 || len(col.Values) != col.Tail {
			return fmt.Errorf("factor column %s tail=%d values=%d mismatch", name, col.Tail, len(col.Values))
		}
		for i, value := range col.Values {
			if !finiteFactorValue(value) {
				return fmt.Errorf("factor column %s value[%d] is not finite numeric", name, i)
			}
		}
	}
	return nil
}

func finiteFactorValue(value any) bool {
	switch n := value.(type) {
	case float64:
		return !math.IsNaN(n) && !math.IsInf(n, 0)
	case float32:
		return !math.IsNaN(float64(n)) && !math.IsInf(float64(n), 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func inputColumns(specs []engine.FactorSpec) []string {
	set := map[string]struct{}{}
	out := append([]string(nil), storageio.KLineColumns...)
	for _, column := range out {
		set[column] = struct{}{}
	}
	for _, spec := range specs {
		for _, column := range spec.ExtraColumns {
			if _, ok := set[column]; ok {
				continue
			}
			set[column] = struct{}{}
			out = append(out, column)
		}
	}
	return out
}

// Status returns current queue and failure counters.
func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	depth := 0
	for _, q := range s.queues {
		depth += len(q)
	}
	s.mu.Unlock()
	return Status{
		QueueDepth:        depth,
		SupersedeCount:    s.supersedeCount.Load(),
		WritebackFailures: s.writebackFailures.Load(),
	}
}

func (s *Service) record(ctx context.Context, task Task, status string, errMsg string, elapsedMS int64, err error) error {
	logRun(ctx, fmt.Sprintf("factor_run_done task_id=%s trigger_type=%s space_id=%s source_dataset=%s target_dataset=%s subject_id=%s freq=%s bar_time=%s factor_count=%d status=%s elapsed_ms=%d error=%q",
		task.TaskID, task.TriggerType, task.SpaceID, task.SourceDataset, task.TargetDataset, task.SubjectID, task.Freq,
		task.BarTime.UTC().Format(time.RFC3339), len(task.Factors), status, elapsedMS, errMsg))
	s.notifyCompletion(ctx, task, TaskResult{
		TaskID:       task.TaskID,
		Status:       status,
		Error:        err,
		ErrorMessage: errMsg,
		ElapsedMS:    elapsedMS,
	})
	return nil
}

func (s *Service) notifyCompletion(ctx context.Context, task Task, result TaskResult) {
	if task.Completion == nil {
		return
	}
	select {
	case task.Completion <- result:
	default:
		log.WarnContextf(ctx, "factor task completion dropped task_id=%s status=%s", result.TaskID, result.Status)
	}
}

func isRetryable(err error) bool {
	var retry engine.RetryableError
	return errors.As(err, &retry)
}
