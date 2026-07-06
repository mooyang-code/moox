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

// RunRecorder stores terminal run records.
type RunRecorder interface {
	Insert(ctx context.Context, run domain.FactorRun) error
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
	runs              RunRecorder
	drainMu           sync.Mutex
	mu                sync.Mutex
	queues            [][]queueItem
	pending           map[taskKey]Task
	supersedeCount    atomic.Int64
	writebackFailures atomic.Int64
}

// NewService creates a scheduler.
func NewService(cfg Config, storage StorageIO, exec engine.Executor, runs RunRecorder) *Service {
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
		runs:    runs,
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
			_ = s.record(ctx, old, domain.RunStatusSuperseded, "superseded by newer bar", 0)
			s.pending[key] = task
			shard := HashSubject(task.SubjectID, s.cfg.Workers)
			s.queues[shard] = append(s.queues[shard], queueItem{task: task})
			return
		}
		s.supersedeCount.Add(1)
		_ = s.record(ctx, task, domain.RunStatusSuperseded, "older than pending task", 0)
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

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		item, ok := s.popNext()
		if !ok {
			return nil
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
		s.executeWithRetry(ctx, item.task)
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

func (s *Service) executeWithRetry(ctx context.Context, task Task) {
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetry; attempt++ {
		elapsed, err := s.executeOnce(ctx, task)
		if err == nil {
			_ = s.record(ctx, task, domain.RunStatusSucceeded, "", elapsed)
			return
		}
		lastErr = err
		if !isRetryable(err) {
			break
		}
	}
	_ = s.record(ctx, task, domain.RunStatusFailed, lastErr.Error(), 0)
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

func (s *Service) record(ctx context.Context, task Task, status string, errMsg string, elapsedMS int64) error {
	if s.runs == nil {
		return nil
	}
	return s.runs.Insert(ctx, domain.FactorRun{
		RunID:         fmt.Sprintf("%s-%s-%d", task.TaskID, status, time.Now().UnixNano()),
		TriggerType:   task.TriggerType,
		SpaceID:       task.SpaceID,
		SourceDataset: task.SourceDataset,
		TargetDataset: task.TargetDataset,
		SubjectID:     task.SubjectID,
		Freq:          task.Freq,
		BarTime:       task.BarTime.UTC().Format(time.RFC3339),
		FactorCount:   len(task.Factors),
		Status:        status,
		Error:         errMsg,
		ElapsedMS:     elapsedMS,
	})
}

func isRetryable(err error) bool {
	var retry engine.RetryableError
	return errors.As(err, &retry)
}
