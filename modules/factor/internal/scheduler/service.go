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
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
)

const maxTargetRowsPerChunk = 2000

var ErrQueueFull = errors.New("factor scheduler queue is full")
var ErrViewIncomplete = errors.New("factor source view is incomplete")

type Config struct {
	Workers                int
	QueueCapacity          int
	MaxRetry               int
	ViewSettleDelay        time.Duration
	EventReadRetry         int
	EventReadRetryInterval time.Duration
}

type StorageIO interface {
	ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error)
	ExpandEndByPeriods(context.Context, storageio.WindowKey, time.Time, int) (time.Time, error)
	WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error)
}

type DatasetRunObserver interface {
	ObserveRun(report.DatasetObservation) error
}

type Option func(*Service)

func WithDatasetMetrics(metrics DatasetRunObserver) Option {
	return func(service *Service) {
		service.metrics = metrics
	}
}

type Status struct {
	QueueDepth         int
	QueueOverflowCount int64
}

type Service struct {
	cfg          Config
	storage      StorageIO
	exec         engine.Executor
	metrics      DatasetRunObserver
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

func NewService(cfg Config, storage StorageIO, exec engine.Executor, opts ...Option) *Service {
	if cfg.Workers <= 0 {
		cfg.Workers = 1
	}
	if cfg.QueueCapacity <= 0 {
		cfg.QueueCapacity = 2048
	}
	if cfg.MaxRetry < 0 {
		cfg.MaxRetry = 0
	}
	if cfg.EventReadRetry < 0 {
		cfg.EventReadRetry = 0
	}
	if cfg.ViewSettleDelay < 0 {
		cfg.ViewSettleDelay = 0
	}
	if cfg.EventReadRetryInterval < 0 {
		cfg.EventReadRetryInterval = 0
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
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
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
		if task.StartTime.Before(current.StartTime) {
			current.StartTime = task.StartTime
		}
		if task.EndTime.After(current.EndTime) {
			current.EndTime = task.EndTime
		}
		current.Factor = task.Factor
		current.LookbackPeriods = task.LookbackPeriods
		current.TaskID = DeterministicTaskID(current)
		s.pending[key] = current
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
	if ctx == nil {
		ctx = context.Background()
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
			task, ok := s.popShard(shard, true)
			if !ok {
				break
			}
			if err := s.Run(s.workerCtx, task); err != nil {
				log.ErrorContextf(s.workerCtx, "factor realtime task failed task_id=%s error=%v", task.TaskID, err)
			}
			s.running.Add(-1)
		}
	}
}

func (s *Service) popShard(shard int, markRunning bool) (Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if shard < 0 || shard >= len(s.queues) || len(s.queues[shard]) == 0 {
		return Task{}, false
	}
	key := s.queues[shard][0]
	s.queues[shard] = s.queues[shard][1:]
	task, ok := s.pending[key]
	if ok {
		if markRunning {
			// Mark the task as running before removing it from pending so
			// WaitIdle can never observe an empty scheduler between states.
			s.running.Add(1)
		}
		delete(s.pending, key)
	}
	return task, ok
}

// Drain synchronously drains queued realtime work. It is intended for tests.
func (s *Service) Drain(ctx context.Context) error {
	if s.started.Load() {
		return s.WaitIdle(ctx)
	}
	var first error
	for {
		var task Task
		var ok bool
		for shard := range s.queues {
			if task, ok = s.popShard(shard, false); ok {
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
	chunks := 0
	var runErr error
	defer func() {
		status := "succeeded"
		if runErr != nil {
			status = "failed"
			result := "error"
			if errors.Is(runErr, ErrViewIncomplete) {
				result = "incomplete"
			}
			s.observeDatasetRun(ctx, report.DatasetObservation{
				Key: report.DatasetKey{
					SpaceID: task.SpaceID, DatasetID: task.TargetDataset, Freq: task.Freq,
				},
				Result: result, FinishedAt: time.Now().UTC(),
			})
		}
		log.InfoContextf(ctx, "factor_task_done task_id=%s trigger_type=%s space_id=%s source_dataset=%s target_dataset=%s subject_id=%s freq=%s start_time=%s end_time=%s factor_id=%s chunk_count=%d status=%s task_elapsed_ms=%d error=%q",
			task.TaskID, task.TriggerType, task.SpaceID, task.SourceDataset, task.TargetDataset,
			task.SubjectID, task.Freq, task.StartTime.UTC().Format(time.RFC3339Nano),
			task.EndTime.UTC().Format(time.RFC3339Nano), task.Factor.FactorID, chunks, status,
			time.Since(started).Milliseconds(), errorString(runErr))
	}()
	if task.TriggerType == "event" {
		if err := sleepContext(ctx, s.cfg.ViewSettleDelay); err != nil {
			runErr = err
			return runErr
		}
		runErr = s.withRetry(ctx, func() error {
			expanded, err := s.storage.ExpandEndByPeriods(ctx, storageio.WindowKey{
				SpaceID: task.SpaceID, SourceDataset: task.SourceDataset,
				SubjectID: task.SubjectID, Freq: task.Freq,
			}, task.EndTime, task.LookbackPeriods-1)
			if err != nil {
				return engine.RetryableError{Err: err}
			}
			if expanded.After(task.EndTime) {
				task.EndTime = expanded
			}
			return nil
		})
		if runErr != nil {
			return runErr
		}
	}
	cursor := task.StartTime
	for cursor.Before(task.EndTime) {
		chunk, err := s.readReadyChunk(ctx, task, cursor)
		if err != nil {
			runErr = err
			return runErr
		}
		if chunk == nil || len(chunk.TargetPeriods) == 0 {
			s.observeDatasetRun(ctx, report.DatasetObservation{
				Key: report.DatasetKey{
					SpaceID: task.SpaceID, DatasetID: task.TargetDataset, Freq: task.Freq,
				},
				Result: "empty", FinishedAt: time.Now().UTC(),
			})
			return nil
		}
		var result *engine.FactorResult
		runErr = s.withRetry(ctx, func() error {
			chunkTask := task.FactorTask
			chunkTask.StartTime = chunk.TargetPeriods[0]
			chunkTask.EndTime = chunk.TargetPeriods[len(chunk.TargetPeriods)-1].Add(time.Nanosecond)
			result, err = s.exec.Execute(ctx, &chunkTask, chunk.Frame)
			if err != nil {
				var nonRetryable engine.NonRetryableError
				if errors.As(err, &nonRetryable) {
					return err
				}
				return engine.RetryableError{Err: err}
			}
			if err := validateFactorResult(task.Factor, chunkTask.StartTime, chunkTask.EndTime, result); err != nil {
				return engine.NonRetryableError{Err: err}
			}
			rowsWritten, err := s.storage.WriteFactorPatch(ctx, &chunkTask, result)
			if err != nil {
				return engine.RetryableError{Err: err}
			}
			observation := report.DatasetObservation{
				Key: report.DatasetKey{
					SpaceID: task.SpaceID, DatasetID: task.TargetDataset, Freq: task.Freq,
				},
				Result: "success", Rows: rowsWritten, FinishedAt: time.Now().UTC(),
			}
			if rowsWritten == 0 {
				observation.Result = "empty"
			} else {
				watermark := maxTime(chunk.TargetPeriods)
				observation.InputWatermark = watermark
				observation.OutputWatermark = watermark
			}
			s.observeDatasetRun(ctx, observation)
			return nil
		})
		if runErr != nil {
			return runErr
		}
		chunks++
		cursor = chunk.TargetPeriods[len(chunk.TargetPeriods)-1].Add(time.Nanosecond)
	}
	return nil
}

func (s *Service) readReadyChunk(ctx context.Context, task Task, cursor time.Time) (*storageio.RangeChunk, error) {
	attempts := 1
	if task.TriggerType == "event" {
		attempts += s.cfg.EventReadRetry
	}
	for attempt := 0; attempt < attempts; attempt++ {
		var chunk *storageio.RangeChunk
		err := s.withRetry(ctx, func() error {
			var readErr error
			chunk, readErr = s.storage.ReadRangeChunk(ctx, storageio.WindowKey{
				SpaceID: task.SpaceID, SourceDataset: task.SourceDataset,
				SubjectID: task.SubjectID, Freq: task.Freq,
			}, cursor, task.EndTime, task.LookbackPeriods, maxTargetRowsPerChunk,
				append([]string(nil), task.Factor.InputColumns...))
			if readErr != nil {
				return engine.RetryableError{Err: readErr}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if task.TriggerType != "event" {
			if chunk != nil && !chunk.Complete && len(chunk.TargetPeriods) > 0 {
				return nil, ErrViewIncomplete
			}
			return chunk, nil
		}
		if chunk != nil && chunk.Complete {
			return chunk, nil
		}
		if attempt+1 < attempts {
			if err := sleepContext(ctx, s.cfg.EventReadRetryInterval); err != nil {
				return nil, err
			}
		}
	}
	return nil, ErrViewIncomplete
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) observeDatasetRun(ctx context.Context, observation report.DatasetObservation) {
	if s == nil || s.metrics == nil {
		return
	}
	if err := s.metrics.ObserveRun(observation); err != nil {
		log.WarnContextf(
			ctx,
			"factor dataset metrics observe failed space_id=%s dataset_id=%s freq=%s result=%s: %v",
			observation.Key.SpaceID, observation.Key.DatasetID, observation.Key.Freq, observation.Result, err,
		)
	}
}

func maxTime(items []time.Time) time.Time {
	var maximum time.Time
	for _, item := range items {
		if maximum.IsZero() || item.After(maximum) {
			maximum = item
		}
	}
	return maximum
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

func validateFactorResult(
	spec engine.FactorSpec,
	startTime, endTime time.Time,
	result *engine.FactorResult,
) error {
	if result == nil {
		return errors.New("nil factor result")
	}
	expected := make(map[string]struct{}, len(spec.Outputs))
	for _, output := range spec.Outputs {
		expected[output] = struct{}{}
	}
	seen := make(map[string]struct{}, len(result.Rows))
	for rowIndex, row := range result.Rows {
		if row.DataTime.Before(startTime) || !row.DataTime.Before(endTime) {
			return fmt.Errorf("factor result row %d is outside target range", rowIndex)
		}
		identity := fmt.Sprintf("%d\x00%s", row.DataTime.UTC().UnixNano(), row.SeriesTag)
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("duplicate factor result identity data_time=%s series_tag=%q",
				row.DataTime.UTC().Format(time.RFC3339Nano), row.SeriesTag)
		}
		seen[identity] = struct{}{}
		if len(row.Values) != len(expected) {
			return fmt.Errorf("factor result row %d outputs=%d expected=%d", rowIndex, len(row.Values), len(expected))
		}
		for name, value := range row.Values {
			if _, ok := expected[name]; !ok {
				return fmt.Errorf("unexpected factor output column %s", name)
			}
			if !validFactorValue(value) {
				return fmt.Errorf("factor output %s row[%d] is not finite numeric or null", name, rowIndex)
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
