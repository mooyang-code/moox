package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

const maxTargetBarsPerChunk = 2000

var ErrQueueFull = errors.New("factor scheduler queue is full")

type Config struct {
	Workers       int
	QueueCapacity int
	MaxRetry      int
}

type StorageIO interface {
	ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error)
	WriteFactorPatch(context.Context, *engine.FactorTask, []time.Time, *engine.FactorResult) error
}

type Status struct {
	QueueDepth         int
	QueueOverflowCount int64
}

type Service struct {
	cfg          Config
	storage      StorageIO
	exec         engine.Executor
	mu           sync.Mutex
	queues       [][]taskKey
	pending      map[taskKey]Task
	overflow     atomic.Int64
	running      atomic.Int64
	started      atomic.Bool
	workerCtx    context.Context
	workerCancel context.CancelFunc
	workerWG     sync.WaitGroup
	wake         []chan struct{}
}

func NewService(cfg Config, storage StorageIO, exec engine.Executor) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 2048
	}
	if cfg.MaxRetry < 0 {
		cfg.MaxRetry = 0
	}
	s := &Service{
		cfg: cfg, storage: storage, exec: exec,
		queues:  make([][]taskKey, cfg.Workers),
		pending: map[taskKey]Task{},
		wake:    make([]chan struct{}, cfg.Workers),
	}
	for i := range s.wake {
		s.wake[i] = make(chan struct{}, 1)
	}
	return s
}

func (s *Service) Enqueue(ctx context.Context, task Task) error {
	if s == nil {
		return errors.New("scheduler is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key := keyOf(task)
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.pending[key]; ok {
		if !task.EndTime.Before(current.EndTime) {
			s.pending[key] = task
		}
		return nil
	}
	if len(s.pending) >= s.cfg.QueueCapacity {
		s.overflow.Add(1)
		return ErrQueueFull
	}
	shard := HashSubject(task.SubjectID, s.cfg.Workers)
	s.pending[key] = task
	s.queues[shard] = append(s.queues[shard], key)
	select {
	case s.wake[shard] <- struct{}{}:
	default:
	}
	return nil
}

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
			task, ok := s.popShard(shard)
			if !ok {
				break
			}
			s.running.Add(1)
			if err := s.Run(s.workerCtx, task); err != nil {
				log.ErrorContextf(s.workerCtx, "factor realtime task failed task_id=%s error=%v", task.TaskID, err)
			}
			s.running.Add(-1)
		}
	}
}

func (s *Service) popShard(shard int) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if shard < 0 || shard >= len(s.queues) || len(s.queues[shard]) == 0 {
		return Task{}, false
	}
	key := s.queues[shard][0]
	s.queues[shard] = s.queues[shard][1:]
	task, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	return task, ok
}

// Drain synchronously drains queued realtime work. It is intended for tests.
func (s *Service) Drain(ctx context.Context) error {
	var first error
	for {
		var task Task
		var ok bool
		for shard := range s.queues {
			if task, ok = s.popShard(shard); ok {
				break
			}
		}
		if !ok {
			return first
		}
		if err := s.Run(ctx, task); err != nil && first == nil {
			first = err
		}
	}
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

// Run synchronously calculates a time range in bounded target-row chunks.
func (s *Service) Run(ctx context.Context, task Task) error {
	if s == nil || s.storage == nil || s.exec == nil {
		return errors.New("scheduler dependencies are required")
	}
	if task.StartTime.IsZero() || task.EndTime.IsZero() || !task.StartTime.Before(task.EndTime) {
		return errors.New("valid start_time and end_time are required")
	}
	started := time.Now()
	cursor := task.StartTime
	chunks := 0
	var runErr error
	defer func() {
		status := "succeeded"
		if runErr != nil {
			status = "failed"
		}
		log.InfoContextf(ctx, "factor_task_done task_id=%s trigger_type=%s space_id=%s source_dataset=%s target_dataset=%s subject_id=%s freq=%s start_time=%s end_time=%s factor_count=%d chunk_count=%d status=%s task_elapsed_ms=%d error=%q",
			task.TaskID, task.TriggerType, task.SpaceID, task.SourceDataset, task.TargetDataset,
			task.SubjectID, task.Freq, task.StartTime.UTC().Format(time.RFC3339Nano),
			task.EndTime.UTC().Format(time.RFC3339Nano), len(task.Factors), chunks, status,
			time.Since(started).Milliseconds(), errorString(runErr))
	}()
	for cursor.Before(task.EndTime) {
		var chunk *storageio.RangeChunk
		var result *engine.FactorResult
		runErr = s.withRetry(ctx, func() error {
			var err error
			chunk, err = s.storage.ReadRangeChunk(ctx, storageio.WindowKey{
				SpaceID: task.SpaceID, SourceDataset: task.SourceDataset,
				SubjectID: task.SubjectID, Freq: task.Freq,
			}, cursor, task.EndTime, task.LookbackBars, maxTargetBarsPerChunk, inputColumns(task.Factors))
			if err != nil {
				return engine.RetryableError{Err: err}
			}
			if chunk == nil || len(chunk.TargetTimes) == 0 {
				return nil
			}
			chunkTask := task.FactorTask
			chunkTask.StartTime = chunk.TargetTimes[0]
			chunkTask.EndTime = chunk.TargetTimes[len(chunk.TargetTimes)-1].Add(time.Nanosecond)
			result, err = s.exec.Execute(ctx, &chunkTask, chunk.Frame)
			if err != nil {
				return err
			}
			if err := validateFactorResult(task.Factors, len(chunk.TargetTimes), result); err != nil {
				return engine.NonRetryableError{Err: err}
			}
			if err := s.storage.WriteFactorPatch(ctx, &chunkTask, chunk.TargetTimes, result); err != nil {
				return engine.RetryableError{Err: err}
			}
			return nil
		})
		if runErr != nil {
			return runErr
		}
		if chunk == nil || len(chunk.TargetTimes) == 0 {
			if chunks == 0 {
				runErr = errors.New("no target rows in requested range")
				return runErr
			}
			return nil
		}
		chunks++
		cursor = chunk.TargetTimes[len(chunk.TargetTimes)-1].Add(time.Nanosecond)
	}
	return nil
}

func (s *Service) withRetry(ctx context.Context, fn func() error) error {
	var last error
	for attempt := 0; attempt <= s.cfg.MaxRetry; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		last = fn()
		if last == nil || !isRetryable(last) {
			return last
		}
	}
	return last
}

func validateFactorResult(specs []engine.FactorSpec, targetRows int, result *engine.FactorResult) error {
	if result == nil {
		return errors.New("nil factor result")
	}
	expected := map[string]struct{}{}
	for _, spec := range specs {
		for _, period := range spec.Periods {
			expected[fmt.Sprintf("%s_%d", spec.Name, period)] = struct{}{}
		}
	}
	if len(result.Columns) != len(expected) {
		return fmt.Errorf("factor result columns=%d expected=%d", len(result.Columns), len(expected))
	}
	for name, values := range result.Columns {
		if _, ok := expected[name]; !ok {
			return fmt.Errorf("unexpected factor output column %s", name)
		}
		if len(values) != targetRows {
			return fmt.Errorf("factor column %s values=%d target_rows=%d", name, len(values), targetRows)
		}
		for i, value := range values {
			if !validFactorValue(value) {
				return fmt.Errorf("factor column %s value[%d] is not finite numeric or null", name, i)
			}
		}
	}
	return nil
}

func validFactorValue(value any) bool {
	if value == nil {
		return true
	}
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
	out := append([]string(nil), storageio.KLineColumns...)
	seen := make(map[string]struct{}, len(out))
	for _, column := range out {
		seen[column] = struct{}{}
	}
	for _, spec := range specs {
		for _, column := range spec.Depends {
			if _, ok := seen[column]; ok {
				continue
			}
			seen[column] = struct{}{}
			out = append(out, column)
		}
	}
	return out
}

func (s *Service) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	depth := len(s.pending)
	s.mu.Unlock()
	return Status{QueueDepth: depth, QueueOverflowCount: s.overflow.Load()}
}

func isRetryable(err error) bool {
	var retry engine.RetryableError
	return errors.As(err, &retry)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
