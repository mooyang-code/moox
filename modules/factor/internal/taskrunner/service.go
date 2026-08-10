package taskrunner

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
)

const maxTargetRowsPerChunk = 2000
const maxRetry = 1

var ErrStaleTask = errors.New("factor task is stale")

type StorageIO interface {
	ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error)
	WriteFactorPatch(context.Context, *engine.FactorTask, *engine.FactorResult) (uint64, error)
}

type periodStorageIO interface {
	ReadPeriodChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, []string) (*storageio.RangeChunk, error)
}

type DatasetRunObserver interface {
	ObserveRun(report.DatasetObservation) error
}

type Option func(*Service)

type TaskValidator func(context.Context, Task) error

func WithFactorGate(gate *FactorGate) Option {
	return func(service *Service) {
		service.factorGate = gate
	}
}

func WithTaskValidator(validator TaskValidator) Option {
	return func(service *Service) {
		service.taskValidator = validator
	}
}

func WithDatasetMetrics(metrics DatasetRunObserver) Option {
	return func(service *Service) {
		service.metrics = metrics
	}
}

func WithViewReadConfig(workers int, timeout time.Duration) Option {
	return func(service *Service) {
		if workers > 0 {
			service.viewReadWorkers = workers
		}
		if timeout > 0 {
			service.viewReadTimeout = timeout
		}
	}
}

type Status struct {
	Workers      int
	ActiveTasks  int
	PendingTasks int
}

type Service struct {
	workers         int
	storage         StorageIO
	exec            engine.Executor
	metrics         DatasetRunObserver
	factorGate      *FactorGate
	taskValidator   TaskValidator
	viewReadWorkers int
	viewReadTimeout time.Duration
	statusMu        sync.Mutex
	active          int
	pending         int
}

func NewService(workers int, storage StorageIO, exec engine.Executor, opts ...Option) *Service {
	if workers < 1 {
		workers = 1
	}
	s := &Service{
		workers: workers, storage: storage, exec: exec, factorGate: NewFactorGate(),
		viewReadWorkers: 1, viewReadTimeout: 10 * time.Second,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Run synchronously calculates a time range in bounded target-row chunks.
func (s *Service) Run(ctx context.Context, task Task) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		return errors.New("task runner is nil")
	}
	s.addActive(1)
	defer s.addActive(-1)
	return s.run(ctx, task)
}

func (s *Service) run(ctx context.Context, task Task) error {
	return s.runWithPeriodRead(ctx, task, nil)
}

func (s *Service) runWithPeriodRead(ctx context.Context, task Task, prepared *storageio.RangeChunk) error {
	release := func() {}
	if s.factorGate != nil {
		release = s.factorGate.AcquireRun(task.Factor.FactorID)
	}
	defer release()
	if s.taskValidator != nil {
		if err := s.taskValidator(ctx, task); err != nil {
			return err
		}
	}
	return s.runValidated(ctx, task, prepared)
}

func (s *Service) executePrepared(ctx context.Context, item preparedTask) error {
	return s.runWithPeriodRead(ctx, item.task, item.shared)
}

// RunAll overlaps bounded View reads with Python execution. Period tasks share
// one View read per exact source window; generic range tasks retain Run's
// synchronous chunked behavior.
func (s *Service) RunAll(ctx context.Context, tasks []Task) []Result {
	results := make([]Result, len(tasks))
	for index := range tasks {
		results[index].Task = tasks[index]
	}
	if len(tasks) == 0 {
		return results
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil {
		err := errors.New("task runner is nil")
		for index := range results {
			results[index].Err = err
		}
		return results
	}

	workerCount := min(s.workers, len(tasks))
	prepared := make(chan preparedTask, max(1, 2*s.workers))
	groups, singles := buildPeriodReadGroups(tasks)
	s.addPending(len(tasks))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range prepared {
				s.startPendingTask()
				if item.shared == nil {
					results[item.index].Err = s.runWithPeriodRead(ctx, item.task, nil)
				} else {
					results[item.index].Err = s.executePrepared(ctx, item)
				}
				s.addActive(-1)
			}
		}()
	}
	for _, single := range singles {
		select {
		case prepared <- preparedTask{index: single.index, task: single.task}:
		case <-ctx.Done():
			results[single.index].Err = ctx.Err()
			s.finishPendingTask()
		}
	}
	s.runPeriodReadPipeline(ctx, groups, prepared, results)
	close(prepared)
	workers.Wait()
	return results
}

func (s *Service) runValidated(ctx context.Context, task Task, prepared *storageio.RangeChunk) error {
	if s == nil || s.storage == nil || s.exec == nil {
		return errors.New("task runner dependencies are required")
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
			s.observeDatasetRun(ctx, report.DatasetObservation{
				Key: report.DatasetKey{
					SpaceID: task.SpaceID, DatasetID: taskResultDataset(task), Freq: task.Freq,
				},
				Result: result, FinishedAt: time.Now().UTC(),
			})
		}
		log.InfoContextf(ctx, "factor_task_done task_id=%s trigger_type=%s space_id=%s source_view_id=%s result_dataset_id=%s subject_id=%s freq=%s start_time=%s end_time=%s factor_id=%s chunk_count=%d status=%s task_elapsed_ms=%d error=%q",
			task.TaskID, task.TriggerType, task.SpaceID, taskSourceView(task), taskResultDataset(task),
			task.SubjectID, task.Freq, task.StartTime.UTC().Format(time.RFC3339Nano),
			task.EndTime.UTC().Format(time.RFC3339Nano), task.Factor.FactorID, chunks, status,
			time.Since(started).Milliseconds(), errorString(runErr))
	}()
	cursor := task.StartTime
	for cursor.Before(task.EndTime) {
		// ViewSourcePeriodReady is the upstream completeness contract. The
		// task runner performs one read and starts the factor immediately; it does
		// not poll a legacy dataset or wait for a second "settled" snapshot.
		chunk, err := s.readChunk(ctx, task, cursor, prepared)
		if err != nil {
			runErr = err
			return runErr
		}
		if chunk == nil || len(chunk.TargetPeriods) == 0 {
			// An acknowledged period can legitimately have no readable rows (for
			// example a degraded subject or a filtered-out correction). Treat that
			// as an empty result and run the normal write plan so the previous
			// manifest is cleared instead of leaving stale factor values visible.
			if task.TriggerType == "view_ready" {
				if _, clearErr := s.storage.WriteFactorPatch(ctx, &task.FactorTask, &engine.FactorResult{}); clearErr != nil {
					runErr = clearErr
					return clearErr
				}
			}
			s.observeDatasetRun(ctx, report.DatasetObservation{
				Key: report.DatasetKey{
					SpaceID: task.SpaceID, DatasetID: taskResultDataset(task), Freq: task.Freq,
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
			// Manual range runs have no source period identity. Give each chunk
			// its own manifest/write identity so a later chunk cannot clear the
			// rows written by an earlier chunk.
			if chunkTask.PeriodTime == 0 {
				chunkTask.PeriodTime = chunk.TargetPeriods[0].Unix()
			}
			result, err = s.exec.Execute(ctx, &chunkTask, chunk.Frame)
			if err != nil {
				var nonRetryable engine.NonRetryableError
				if errors.As(err, &nonRetryable) {
					return err
				}
				return engine.RetryableError{Err: err}
			}
			// The Python contract receives lookback rows as context and may
			// return values for that full window. Only rows in this task's target
			// range belong to the current period write; historical rows must not
			// fail validation or overwrite an earlier period's manifest.
			result = filterTargetResult(result, chunkTask.StartTime, chunkTask.EndTime)
			if err := validateFactorResult(task.Factor, chunkTask.StartTime, chunkTask.EndTime, result); err != nil {
				return engine.NonRetryableError{Err: err}
			}
			rowsWritten, err := s.storage.WriteFactorPatch(ctx, &chunkTask, result)
			if err != nil {
				return engine.RetryableError{Err: err}
			}
			observation := report.DatasetObservation{
				Key: report.DatasetKey{
					SpaceID: task.SpaceID, DatasetID: taskResultDataset(task), Freq: task.Freq,
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
		// View-ready/recalc tasks represent exactly one period. The chunk may
		// contain several series tags for that period; advancing by one
		// nanosecond and reading again would see an empty tail and clear the
		// values we just wrote.
		if task.PeriodTime > 0 {
			return nil
		}
		cursor = chunk.TargetPeriods[len(chunk.TargetPeriods)-1].Add(time.Nanosecond)
	}
	return nil
}

func (s *Service) readChunk(ctx context.Context, task Task, cursor time.Time, prepared *storageio.RangeChunk) (*storageio.RangeChunk, error) {
	if prepared != nil {
		return projectRangeChunk(prepared, task.Factor.InputColumns)
	}
	return s.readChunkColumns(ctx, task, cursor, task.Factor.InputColumns)
}

func (s *Service) readChunkColumns(ctx context.Context, task Task, cursor time.Time, columns []string) (*storageio.RangeChunk, error) {
	var chunk *storageio.RangeChunk
	err := s.withRetry(ctx, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, s.viewReadTimeout)
		defer cancel()
		var readErr error
		key := storageio.WindowKey{
			SpaceID: task.SpaceID, SourceViewID: taskSourceView(task), SourceDataset: task.SourceDataset,
			SubjectID: task.SubjectID, Freq: task.Freq,
		}
		if periodReader, ok := s.storage.(periodStorageIO); ok && task.PeriodTime > 0 {
			chunk, readErr = periodReader.ReadPeriodChunk(
				attemptCtx, key, cursor, task.EndTime, task.LookbackPeriods, append([]string(nil), columns...),
			)
		} else {
			chunk, readErr = s.storage.ReadRangeChunk(
				attemptCtx, key, cursor, task.EndTime, task.LookbackPeriods, maxTargetRowsPerChunk,
				append([]string(nil), columns...),
			)
		}
		if readErr != nil {
			var nonRetryable engine.NonRetryableError
			if errors.As(readErr, &nonRetryable) {
				return readErr
			}
			return engine.RetryableError{Err: readErr}
		}
		return nil
	})
	return chunk, err
}

func projectRangeChunk(chunk *storageio.RangeChunk, columns []string) (*storageio.RangeChunk, error) {
	projected := &storageio.RangeChunk{
		TargetPeriods: append([]time.Time(nil), chunk.TargetPeriods...),
		Complete:      chunk.Complete,
		IndexedTo:     chunk.IndexedTo,
	}
	if chunk.Frame == nil {
		return projected, nil
	}
	indexes := make([]int, len(columns))
	for index, column := range columns {
		indexes[index] = -1
		for sourceIndex, sourceColumn := range chunk.Frame.Columns {
			if sourceColumn == column {
				indexes[index] = sourceIndex
				break
			}
		}
		if indexes[index] < 0 {
			return nil, fmt.Errorf("shared View read is missing factor input column %q", column)
		}
	}
	frame := &engine.DataFrame{
		Columns:    append([]string(nil), columns...),
		Rows:       make([][]any, len(chunk.Frame.Rows)),
		DataTimes:  append([]time.Time(nil), chunk.Frame.DataTimes...),
		SeriesTags: append([]string(nil), chunk.Frame.SeriesTags...),
	}
	for rowIndex, row := range chunk.Frame.Rows {
		frame.Rows[rowIndex] = make([]any, len(indexes))
		for columnIndex, sourceIndex := range indexes {
			if sourceIndex >= len(row) {
				return nil, fmt.Errorf("shared View row %d is missing column %q", rowIndex, columns[columnIndex])
			}
			frame.Rows[rowIndex][columnIndex] = row[sourceIndex]
		}
	}
	projected.Frame = frame
	return projected, nil
}

func taskSourceView(task Task) string {
	if task.SourceViewID != "" {
		return task.SourceViewID
	}
	return task.SourceDataset
}
func taskResultDataset(task Task) string {
	if task.ResultDatasetID != "" {
		return task.ResultDatasetID
	}
	return task.TargetDataset
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
	for attempt := 0; attempt <= maxRetry; attempt++ {
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

func filterTargetResult(result *engine.FactorResult, startTime, endTime time.Time) *engine.FactorResult {
	if result == nil {
		return nil
	}
	filtered := &engine.FactorResult{Rows: make([]engine.FactorResultRow, 0, len(result.Rows))}
	for _, row := range result.Rows {
		if row.DataTime.Before(startTime) || !row.DataTime.Before(endTime) {
			continue
		}
		filtered.Rows = append(filtered.Rows, row)
	}
	return filtered
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
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return Status{
		Workers:      s.workers,
		ActiveTasks:  s.active,
		PendingTasks: s.pending,
	}
}

func (s *Service) addActive(delta int) {
	s.statusMu.Lock()
	s.active += delta
	s.statusMu.Unlock()
}

func (s *Service) addPending(delta int) {
	s.statusMu.Lock()
	s.pending += delta
	s.statusMu.Unlock()
}

func (s *Service) startPendingTask() {
	s.statusMu.Lock()
	s.pending--
	s.active++
	s.statusMu.Unlock()
}

func (s *Service) finishPendingTask() {
	s.statusMu.Lock()
	s.pending--
	s.statusMu.Unlock()
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
