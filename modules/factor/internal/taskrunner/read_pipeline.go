package taskrunner

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

type periodReadKey struct {
	spaceID, sourceViewID, sourceDataset, subjectID, freq string
	periodTime                                            int64
	triggerType, triggerEventID                           string
	startTime, endTime                                    time.Time
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

func (s *Service) runPeriodReadPipeline(
	ctx context.Context,
	groups []*periodReadGroup,
	prepared chan<- preparedBatch,
	results []Result,
) {
	if len(groups) == 0 {
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
		SpaceID: representative.SpaceID, SourceViewID: taskSourceView(representative),
		SourceDataset: representative.SourceDataset, SubjectID: representative.SubjectID, Freq: representative.Freq,
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
			spaceID: task.SpaceID, sourceViewID: taskSourceView(task), sourceDataset: task.SourceDataset,
			subjectID: task.SubjectID, freq: task.Freq, periodTime: task.PeriodTime,
			triggerType: task.TriggerType, triggerEventID: task.TriggerEventID,
			startTime: task.StartTime, endTime: task.EndTime,
		}
		group := groupsByKey[key]
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
	}
	for _, group := range groups {
		columnSet := make(map[string]struct{})
		for _, member := range group.members {
			for _, column := range member.task.Factor.InputColumns {
				if column = strings.TrimSpace(column); column != "" {
					columnSet[column] = struct{}{}
				}
			}
		}
		group.columns = make([]string, 0, len(columnSet))
		for column := range columnSet {
			group.columns = append(group.columns, column)
		}
		sort.Strings(group.columns)
	}
	return groups, singles
}
