package taskrunner

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

type periodReadKey struct {
	sourceSeriesTag                                                              string
	filterSourceSeriesTag                                                        bool
	inputContractVersion                                                         string
	spaceID, sourceViewID, sourceDataset, subjectID, freq, expectedActiveIndexID string
	expectedActiveIndexRevision                                                  uint64
	periodTime                                                                   int64
	triggerType, triggerEventID                                                  string
	startTime, endTime                                                           time.Time
}

type indexedTask struct {
	index int
	task  Task
}

type periodReadGroup struct {
	key             periodReadKey
	startTime       time.Time
	endTime         time.Time
	lookbackPeriods int
	columns         []string
	members         []indexedTask
	attempt         int
	generation      int
	terminal        bool
}

type readJob struct {
	group      *periodReadGroup
	attempt    int
	generation int
}

type readOutcome struct {
	job   readJob
	chunk *storageio.RangeChunk
	err   error
}

type batchPeriodStorageIO interface {
	ReadPeriodChunks(context.Context, storageio.WindowKey, []string, time.Time, time.Time, int, []string) (map[string]*storageio.RangeChunk, error)
}

type periodReadBatchKey struct {
	triggerType                                                                       string
	sourceSeriesTag                                                                   string
	filterSourceSeriesTag                                                             bool
	inputContractVersion                                                              string
	spaceID, sourceViewID, sourceDataset, freq, expectedActiveIndexID, triggerEventID string
	expectedActiveIndexRevision                                                       uint64
	periodTime                                                                        int64
}

type periodReadBatch struct {
	key      periodReadBatchKey
	groups   []*periodReadGroup
	start    time.Time
	end      time.Time
	lookback int
	columns  []string
}

func (s *Service) runPeriodReadPipeline(
	ctx context.Context,
	groups []*periodReadGroup,
	prepared chan<- preparedBatch,
	results []Result,
) {
	if len(groups) == 0 {
		return
	}
	if batcher, ok := s.storage.(batchPeriodStorageIO); ok {
		s.runBatchedPeriodReadPipeline(ctx, batcher, groups, prepared, results)
		return
	}
	readWorkers := min(max(1, s.viewReadWorkers), len(groups))
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan readJob)
	outcomes := make(chan readOutcome, readWorkers)
	var readers sync.WaitGroup
	for range readWorkers {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for job := range jobs {
				if err := readCtx.Err(); err != nil {
					outcomes <- readOutcome{job: job, err: err}
					continue
				}
				started := time.Now()
				attemptCtx, attemptCancel := context.WithTimeout(readCtx, s.viewReadTimeout)
				if err := attemptCtx.Err(); err != nil {
					attemptCancel()
					outcomes <- readOutcome{job: job, err: err}
					continue
				}
				chunk, err := s.readPeriodGroup(attemptCtx, job.group)
				attemptCancel()
				elapsed := time.Since(started)
				log.InfoContextf(readCtx, "factor_view_read_done space_id=%s source_view_id=%s subject_id=%s freq=%s period_time=%d lookback_periods=%d attempt=%d result=%s elapsed_ms=%d column_count=%d",
					job.group.key.spaceID, job.group.key.sourceViewID, job.group.key.subjectID,
					job.group.key.freq, job.group.key.periodTime, job.group.lookbackPeriods,
					job.attempt, viewReadResult(err), elapsed.Milliseconds(), len(job.group.columns))
				outcomes <- readOutcome{job: job, chunk: chunk, err: err}
			}
		}()
	}

	pending := append([]*periodReadGroup(nil), groups...)
	inflight := 0
	ctxDone := ctx.Done()
	for len(pending) > 0 || inflight > 0 {
		var dispatch chan<- readJob
		var next readJob
		if len(pending) > 0 && inflight < readWorkers && ctx.Err() == nil {
			group := pending[0]
			next = readJob{group: group, attempt: group.attempt + 1, generation: group.generation + 1}
			dispatch = jobs
		}
		select {
		case dispatch <- next:
			pending = pending[1:]
			next.group.attempt = next.attempt
			next.group.generation = next.generation
			inflight++
		case outcome := <-outcomes:
			inflight--
			group := outcome.job.group
			if group.terminal || outcome.job.generation != group.generation {
				continue
			}
			if ctx.Err() != nil {
				s.failReadGroup(group, ctx.Err(), results)
				continue
			}
			if outcome.err != nil {
				if shouldRetryRead(ctx, outcome.err) && group.attempt < 2 {
					log.WarnContextf(ctx, "factor_view_read_retry space_id=%s source_view_id=%s subject_id=%s freq=%s period_time=%d attempt=%d retry_position=tail error=%q",
						group.key.spaceID, group.key.sourceViewID, group.key.subjectID, group.key.freq,
						group.key.periodTime, group.attempt, outcome.err.Error())
					pending = append(pending, group)
					continue
				}
				s.failReadGroup(group, outcome.err, results)
				continue
			}
			select {
			case prepared <- preparedBatch{members: append([]indexedTask(nil), group.members...), shared: outcome.chunk}:
			case <-ctx.Done():
				for _, member := range group.members {
					results[member.index].Err = ctx.Err()
					s.finishPendingTask()
				}
			}
			group.terminal = true
		case <-ctxDone:
			cancel()
			for _, group := range pending {
				s.failReadGroup(group, ctx.Err(), results)
			}
			pending = nil
			ctxDone = nil
		}
	}
	close(jobs)
	readers.Wait()
}

func clusterPeriodReadGroups(groups []*periodReadGroup) []*periodReadBatch {
	index := make(map[periodReadBatchKey]*periodReadBatch)
	out := make([]*periodReadBatch, 0)
	for _, group := range groups {
		if group == nil {
			continue
		}
		key := periodReadBatchKey{
			triggerType:     group.key.triggerType,
			sourceSeriesTag: group.key.sourceSeriesTag, filterSourceSeriesTag: group.key.filterSourceSeriesTag,
			inputContractVersion: group.key.inputContractVersion,
			spaceID:              group.key.spaceID, sourceViewID: group.key.sourceViewID, sourceDataset: group.key.sourceDataset,
			freq: group.key.freq, expectedActiveIndexID: group.key.expectedActiveIndexID,
			expectedActiveIndexRevision: group.key.expectedActiveIndexRevision,
			periodTime:                  group.key.periodTime, triggerEventID: group.key.triggerEventID,
		}
		// Subject events have distinct delivery identities but can share a source
		// snapshot read. Never rewrite their task identities for this optimization.
		if group.key.triggerType == "subject_ready" && group.key.inputContractVersion != "" {
			key.triggerEventID = ""
		}
		batch := index[key]
		if batch != nil && key.inputContractVersion != "" && storagepb.SeriesWindowSubjectLimit(max(1, batch.lookback, group.lookbackPeriods), len(mergeReadColumns(batch.columns, group.columns))) == 0 {
			batch = nil
		}
		if batch == nil {
			batch = &periodReadBatch{
				key: key, start: group.startTime, end: group.endTime, lookback: group.lookbackPeriods,
			}
			index[key] = batch
			out = append(out, batch)
		}
		if group.startTime.Before(batch.start) {
			batch.start = group.startTime
		}
		if group.endTime.After(batch.end) {
			batch.end = group.endTime
		}
		if group.lookbackPeriods > batch.lookback {
			batch.lookback = group.lookbackPeriods
		}
		batch.groups = append(batch.groups, group)
		batch.columns = mergeReadColumns(batch.columns, group.columns)
	}
	return out
}

func (s *Service) runBatchedPeriodReadPipeline(
	ctx context.Context,
	batcher batchPeriodStorageIO,
	groups []*periodReadGroup,
	prepared chan<- preparedBatch,
	results []Result,
) {
	batches := clusterPeriodReadGroups(groups)
	if len(batches) == 0 {
		return
	}
	readWorkers := min(max(1, s.viewReadWorkers), len(batches))
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type batchJob struct {
		batch      *periodReadBatch
		attempt    int
		generation int
	}
	type batchOutcome struct {
		job    batchJob
		chunks map[string]*storageio.RangeChunk
		err    error
	}
	jobs := make(chan batchJob)
	outcomes := make(chan batchOutcome, readWorkers)
	var readers sync.WaitGroup
	for range readWorkers {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for job := range jobs {
				if err := readCtx.Err(); err != nil {
					outcomes <- batchOutcome{job: job, err: err}
					continue
				}
				started := time.Now()
				attemptCtx, attemptCancel := context.WithTimeout(readCtx, s.viewReadTimeout)
				if err := attemptCtx.Err(); err != nil {
					attemptCancel()
					outcomes <- batchOutcome{job: job, err: err}
					continue
				}
				subjectIDs := make([]string, 0, len(job.batch.groups))
				for _, group := range job.batch.groups {
					subjectIDs = append(subjectIDs, group.key.subjectID)
				}
				key := storageio.WindowKey{
					SourceSeriesTag: job.batch.key.sourceSeriesTag, FilterSourceSeriesTag: job.batch.key.filterSourceSeriesTag,
					SpaceID: job.batch.key.spaceID, SourceViewID: job.batch.key.sourceViewID,
					SourceDataset: job.batch.key.sourceDataset, Freq: job.batch.key.freq,
					ExpectedActiveIndexID:       job.batch.key.expectedActiveIndexID,
					ExpectedActiveIndexRevision: job.batch.key.expectedActiveIndexRevision,
				}
				if job.batch.key.triggerType == "subject_ready" {
					key.InputContractVersion = job.batch.key.inputContractVersion
				}
				chunks, err := batcher.ReadPeriodChunks(
					attemptCtx, key, subjectIDs, job.batch.start, job.batch.end,
					job.batch.lookback, append([]string(nil), job.batch.columns...),
				)
				attemptCancel()
				log.InfoContextf(readCtx, "factor_view_read_batch_done space_id=%s source_view_id=%s freq=%s period_time=%d subject_count=%d lookback_periods=%d attempt=%d result=%s elapsed_ms=%d column_count=%d",
					job.batch.key.spaceID, job.batch.key.sourceViewID, job.batch.key.freq, job.batch.key.periodTime,
					len(subjectIDs), job.batch.lookback, job.attempt, viewReadResult(err), time.Since(started).Milliseconds(), len(job.batch.columns))
				outcomes <- batchOutcome{job: job, chunks: chunks, err: err}
			}
		}()
	}

	pending := append([]*periodReadBatch(nil), batches...)
	inflight := 0
	ctxDone := ctx.Done()
	for len(pending) > 0 || inflight > 0 {
		var dispatch chan<- batchJob
		var next batchJob
		if len(pending) > 0 && inflight < readWorkers && ctx.Err() == nil {
			batch := pending[0]
			next = batchJob{batch: batch, attempt: batch.groups[0].attempt + 1, generation: batch.groups[0].generation + 1}
			dispatch = jobs
		}
		select {
		case dispatch <- next:
			pending = pending[1:]
			for _, group := range next.batch.groups {
				group.attempt = next.attempt
				group.generation = next.generation
			}
			inflight++
		case outcome := <-outcomes:
			inflight--
			batch := outcome.job.batch
			stale := false
			for _, group := range batch.groups {
				if group.terminal || outcome.job.generation != group.generation {
					stale = true
					break
				}
			}
			if stale {
				continue
			}
			if ctx.Err() != nil {
				for _, group := range batch.groups {
					s.failReadGroup(group, ctx.Err(), results)
				}
				continue
			}
			if outcome.err != nil {
				if shouldRetryRead(ctx, outcome.err) && outcome.job.attempt < 2 {
					log.WarnContextf(ctx, "factor_view_read_batch_retry space_id=%s source_view_id=%s freq=%s period_time=%d attempt=%d retry_position=tail error=%q",
						batch.key.spaceID, batch.key.sourceViewID, batch.key.freq, batch.key.periodTime, outcome.job.attempt, outcome.err.Error())
					pending = append(pending, batch)
					continue
				}
				for _, group := range batch.groups {
					s.failReadGroup(group, outcome.err, results)
				}
				continue
			}
			for _, group := range batch.groups {
				if group.terminal {
					continue
				}
				chunk := outcome.chunks[group.key.subjectID]
				if chunk == nil {
					s.failReadGroup(group, fmt.Errorf("view read missing subject %s", group.key.subjectID), results)
					continue
				}
				select {
				case prepared <- preparedBatch{members: append([]indexedTask(nil), group.members...), shared: chunk}:
				case <-ctx.Done():
					for _, member := range group.members {
						results[member.index].Err = ctx.Err()
						s.finishPendingTask()
					}
				}
				group.terminal = true
			}
		case <-ctxDone:
			cancel()
			for _, batch := range pending {
				for _, group := range batch.groups {
					s.failReadGroup(group, ctx.Err(), results)
				}
			}
			pending = nil
			ctxDone = nil
		}
	}
	close(jobs)
	readers.Wait()
}

func viewReadResult(err error) string {
	if err == nil {
		return "success"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "error"
}

func (s *Service) readPeriodGroup(ctx context.Context, group *periodReadGroup) (*storageio.RangeChunk, error) {
	representative := group.members[0].task
	key := storageio.WindowKey{
		SourceSeriesTag: representative.SourceSeriesTag, FilterSourceSeriesTag: representative.FilterSourceSeriesTag,
		SpaceID: representative.SpaceID, SourceViewID: taskSourceView(representative),
		SourceDataset: representative.SourceDataset, SubjectID: representative.SubjectID, Freq: representative.Freq,
		ExpectedActiveIndexID:       representative.ExpectedActiveIndexID,
		ExpectedActiveIndexRevision: representative.ExpectedActiveIndexRevision,
	}
	if representative.TriggerType == "subject_ready" {
		key.InputContractVersion = representative.InputContractVersion
	}
	if periodReader, ok := s.storage.(periodStorageIO); ok {
		return periodReader.ReadPeriodChunk(
			ctx, key, group.startTime, group.endTime,
			group.lookbackPeriods, append([]string(nil), group.columns...),
		)
	}
	return s.storage.ReadRangeChunk(
		ctx, key, group.startTime, group.endTime,
		group.lookbackPeriods, maxTargetRowsPerChunk, append([]string(nil), group.columns...),
	)
}

func shouldRetryRead(parent context.Context, err error) bool {
	if err == nil || parent.Err() != nil {
		return false
	}
	var nonRetryable engine.NonRetryableError
	return !errors.As(err, &nonRetryable)
}

func (s *Service) failReadGroup(group *periodReadGroup, err error, results []Result) {
	if group == nil || group.terminal {
		return
	}
	group.terminal = true
	for _, member := range group.members {
		results[member.index].Err = err
		s.finishPendingTask()
	}
}

type preparedBatch struct {
	members []indexedTask
	shared  *storageio.RangeChunk
}

// preparedTask remains as a small projection helper for single-task callers
// and tests; period execution now transports preparedBatch values.
type preparedTask struct {
	index  int
	task   Task
	shared *storageio.RangeChunk
}

// restrictRangeChunkForTask restores the per-member input window when a
// period read group falls back to individual execution. The shared read uses
// the largest lookback so it can serve the batch path; the legacy individual
// path must still see exactly target rows plus N-1 distinct history periods.
func restrictRangeChunkForTask(chunk *storageio.RangeChunk, startTime, endTime time.Time, lookbackPeriods int) (*storageio.RangeChunk, error) {
	if chunk == nil || chunk.Frame == nil {
		return chunk, nil
	}
	frame := chunk.Frame
	if len(frame.Rows) != len(frame.DataTimes) {
		return nil, fmt.Errorf("shared View read has %d rows but %d data times", len(frame.Rows), len(frame.DataTimes))
	}
	if len(frame.SeriesTags) != 0 && len(frame.SeriesTags) != len(frame.Rows) {
		return nil, fmt.Errorf("shared View read has %d rows but %d series tags", len(frame.Rows), len(frame.SeriesTags))
	}

	// Find the latest N-1 distinct periods before the target window. All rows
	// for a retained period (including multiple series tags) stay together.
	historyTimes := make(map[int64]time.Time)
	for _, at := range frame.DataTimes {
		if at.Before(startTime) {
			historyTimes[at.UTC().UnixNano()] = at
		}
	}
	history := make([]time.Time, 0, len(historyTimes))
	for _, at := range historyTimes {
		history = append(history, at)
	}
	sort.Slice(history, func(i, j int) bool { return history[i].Before(history[j]) })
	keep := lookbackPeriods - 1
	if keep < 0 {
		keep = 0
	}
	if keep < len(history) {
		history = history[len(history)-keep:]
	}
	keepTimes := make(map[int64]struct{}, len(history))
	for _, at := range history {
		keepTimes[at.UTC().UnixNano()] = struct{}{}
	}

	indices := make([]int, 0, len(frame.Rows))
	for index, at := range frame.DataTimes {
		utc := at.UTC()
		if (!utc.Before(startTime) && utc.Before(endTime)) || func() bool {
			_, ok := keepTimes[utc.UnixNano()]
			return ok
		}() {
			indices = append(indices, index)
		}
	}
	filtered := &engine.DataFrame{
		Columns:   append([]string(nil), frame.Columns...),
		Rows:      make([][]any, 0, len(indices)),
		DataTimes: make([]time.Time, 0, len(indices)),
	}
	if len(frame.SeriesTags) != 0 {
		filtered.SeriesTags = make([]string, 0, len(indices))
	}
	for _, index := range indices {
		filtered.Rows = append(filtered.Rows, append([]any(nil), frame.Rows[index]...))
		filtered.DataTimes = append(filtered.DataTimes, frame.DataTimes[index])
		if len(frame.SeriesTags) != 0 {
			filtered.SeriesTags = append(filtered.SeriesTags, frame.SeriesTags[index])
		}
	}
	targetPeriods := make([]time.Time, 0, len(chunk.TargetPeriods))
	for _, at := range chunk.TargetPeriods {
		if !at.Before(startTime) && at.Before(endTime) {
			targetPeriods = append(targetPeriods, at)
		}
	}
	return &storageio.RangeChunk{
		Frame:         filtered,
		TargetPeriods: targetPeriods,
		Complete:      chunk.Complete,
		IndexedTo:     chunk.IndexedTo,
	}, nil
}

func (p preparedTask) project() (*storageio.RangeChunk, error) {
	return projectRangeChunk(p.shared, p.task.Factor.InputColumns)
}

func buildPeriodReadGroups(tasks []Task) ([]*periodReadGroup, []indexedTask) {
	groupsByKey := make(map[periodReadKey]*periodReadGroup)
	groups := make([]*periodReadGroup, 0)
	singles := make([]indexedTask, 0)
	for index, task := range tasks {
		member := indexedTask{index: index, task: task}
		if task.PeriodTime <= 0 {
			singles = append(singles, member)
			continue
		}
		key := periodReadKey{
			sourceSeriesTag: task.SourceSeriesTag, filterSourceSeriesTag: task.FilterSourceSeriesTag,
			inputContractVersion: task.InputContractVersion,
			spaceID:              task.SpaceID, sourceViewID: taskSourceView(task), sourceDataset: task.SourceDataset,
			subjectID: task.SubjectID, freq: task.Freq, periodTime: task.PeriodTime,
			expectedActiveIndexID:       task.ExpectedActiveIndexID,
			expectedActiveIndexRevision: task.ExpectedActiveIndexRevision,
			triggerType:                 task.TriggerType, triggerEventID: task.TriggerEventID,
			startTime: task.StartTime, endTime: task.EndTime,
		}
		group := groupsByKey[key]
		if group != nil && key.inputContractVersion != "" && storagepb.SeriesWindowSubjectLimit(max(1, group.lookbackPeriods, task.LookbackPeriods), len(mergeReadColumns(group.columns, task.Factor.InputColumns))) == 0 {
			group = nil
		}
		if group == nil {
			group = &periodReadGroup{key: key, startTime: task.StartTime, endTime: task.EndTime, lookbackPeriods: task.LookbackPeriods}
			groupsByKey[key] = group
			groups = append(groups, group)
		} else {
			if task.StartTime.Before(group.startTime) {
				group.startTime = task.StartTime
			}
			if task.EndTime.After(group.endTime) {
				group.endTime = task.EndTime
			}
			if task.LookbackPeriods > group.lookbackPeriods {
				group.lookbackPeriods = task.LookbackPeriods
			}
		}
		group.members = append(group.members, member)
		group.columns = mergeReadColumns(group.columns, task.Factor.InputColumns)
	}
	return groups, singles
}

func mergeReadColumns(left, right []string) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for _, columns := range [][]string{left, right} {
		for _, column := range columns {
			if column = strings.TrimSpace(column); column != "" {
				set[column] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for column := range set {
		result = append(result, column)
	}
	sort.Strings(result)
	return result
}
