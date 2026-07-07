package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// Config controls scheduler execution.
type Config struct {
	Workers  int
	MaxRetry int
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
	supersedeCount    atomic.Int64
	writebackFailures atomic.Int64
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
	return &Service{
		cfg:     cfg,
		storage: storage,
		exec:    exec,
		queues:  make([][]queueItem, cfg.Workers),
		pending: map[taskKey]Task{},
	}
}

// Enqueue adds a task, replacing older pending work for the same subject scope.
func (s *Service) Enqueue(ctx context.Context, task Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := keyOf(task)
	if old, ok := s.pending[key]; ok {
		if task.BarTime.After(old.BarTime) || task.BarTime.Equal(old.BarTime) {
			s.supersedeCount.Add(1)
			_ = s.record(ctx, old, domain.RunStatusSuperseded, "superseded by newer bar", 0, nil)
			s.pending[key] = task
			shard := HashSubject(task.SubjectID, s.cfg.Workers)
			s.queues[shard] = append(s.queues[shard], queueItem{task: task})
			return
		}
		s.supersedeCount.Add(1)
		_ = s.record(ctx, task, domain.RunStatusSuperseded, "older than pending task", 0, nil)
		return
	}
	s.pending[key] = task
	shard := HashSubject(task.SubjectID, s.cfg.Workers)
	s.queues[shard] = append(s.queues[shard], queueItem{task: task})
}

// Drain synchronously drains all currently queued tasks. Production startup uses
// worker goroutines; tests and run-once use this deterministic path.
func (s *Service) Drain(ctx context.Context) error {
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
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetry; attempt++ {
		elapsed, err := s.executeOnce(ctx, task)
		if err == nil {
			_ = s.record(ctx, task, domain.RunStatusSucceeded, "", elapsed, nil)
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
	result, err := s.exec.Execute(ctx, &task.FactorTask, frame)
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
