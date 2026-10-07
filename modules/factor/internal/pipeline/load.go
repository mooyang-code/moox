package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"trpc.group/trpc-go/trpc-go/log"
)

// warmupRetryDelay spaces warm-up attempts after a read failure, so a Storage
// outage is not hammered while live periods keep re-queueing the subjects.
const warmupRetryDelay = 10 * time.Second

func (r *Runner) Load(ctx context.Context, plan Plan) (LoadResult, error) {
	if r == nil || r.store == nil {
		return LoadResult{}, errors.New("factor Storage store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result := LoadResult{Frames: make(map[string]*storageio.Frame)}
	subjects := uniqueSorted(plan.Available)
	failedSet := make(map[string]struct{}, len(plan.UpstreamFailed))
	for _, subject := range plan.UpstreamFailed {
		failedSet[subject] = struct{}{}
	}
	if len(subjects) == 0 {
		result.FailedSubjects = uniqueSorted(plan.UpstreamFailed)
		return result, nil
	}
	if !plan.TargetStart.Before(plan.TargetEnd) {
		return LoadResult{}, errors.New("factor read window must have start before end")
	}
	live := plan.Mode == ModeLive && r.windows != nil
	var reads map[string]subjectRead
	if live {
		reads, result.WarmingSubjects = r.windows.plan(plan, subjects)
		for _, subject := range result.WarmingSubjects {
			failedSet[subject] = struct{}{}
		}
	} else {
		start, err := readWindowStart(r.clock, plan)
		if err != nil {
			return LoadResult{}, err
		}
		reads = make(map[string]subjectRead, len(subjects))
		for _, subject := range subjects {
			reads[subject] = subjectRead{start: start}
		}
	}

	frames, failed, err := r.readSubjects(ctx, plan, reads, plan.Mode == ModeRecalc)
	if err != nil {
		return LoadResult{}, err
	}
	for subject := range failed {
		failedSet[subject] = struct{}{}
	}
	for subject, frame := range frames {
		if prefix := reads[subject].prefix; len(prefix) > 0 {
			frame.Rows = append(append(make([][]any, 0, len(prefix)+len(frame.Rows)), prefix...), frame.Rows...)
		}
		result.Frames[subject] = frame
	}
	if live {
		r.windows.store(plan, result.Frames)
	}
	for _, subject := range subjects {
		if _, failed := failedSet[subject]; !failed {
			result.Available = append(result.Available, subject)
		}
	}
	for subject := range failedSet {
		result.FailedSubjects = append(result.FailedSubjects, subject)
	}
	result.FailedSubjects = uniqueSorted(result.FailedSubjects)
	return result, nil
}

// readSubjects reads each subject from its own start to the plan's end.
// Subjects sharing a start share batches, read by a bounded worker pool. A
// failed batch marks its subjects failed; only when every batch fails is the
// read an infrastructure error.
func (r *Runner) readSubjects(ctx context.Context, plan Plan, reads map[string]subjectRead, pageDeadline bool) (map[string]*storageio.Frame, map[string]struct{}, error) {
	type readJob struct {
		start    time.Time
		subjects []string
	}
	batchSize := r.cfg.ReadBatchSubjects
	if batchSize < 1 {
		batchSize = 100
	}
	groups := make(map[time.Time][]string)
	var starts []time.Time
	for _, subject := range uniqueSorted(keysOf(reads)) {
		start := reads[subject].start
		if _, seen := groups[start]; !seen {
			starts = append(starts, start)
		}
		groups[start] = append(groups[start], subject)
	}
	var jobs []readJob
	for _, start := range starts {
		group := groups[start]
		for from := 0; from < len(group); from += batchSize {
			to := min(from+batchSize, len(group))
			jobs = append(jobs, readJob{start: start, subjects: append([]string(nil), group[from:to]...)})
		}
	}
	frames := make(map[string]*storageio.Frame, len(reads))
	failed := make(map[string]struct{})
	if len(jobs) == 0 {
		return frames, failed, nil
	}
	workers := max(1, min(r.cfg.ReadWorkers, len(jobs)))
	type batchResult struct {
		subjects []string
		frames   map[string]*storageio.Frame
		err      error
	}
	queue := make(chan readJob)
	results := make(chan batchResult, len(jobs))
	var workersDone sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for job := range queue {
				batch, err := r.readBatch(ctx, plan, job.start, job.subjects, pageDeadline)
				results <- batchResult{subjects: job.subjects, frames: batch, err: err}
			}
		}()
	}
	go func() {
		defer func() {
			close(queue)
			workersDone.Wait()
			close(results)
		}()
		for _, job := range jobs {
			select {
			case queue <- job:
			case <-ctx.Done():
				return
			}
		}
	}()

	failedBatches := 0
	var firstErr error
	for batch := range results {
		if batch.err != nil {
			failedBatches++
			if firstErr == nil {
				firstErr = batch.err
			}
			for _, subject := range batch.subjects {
				failed[subject] = struct{}{}
			}
			continue
		}
		for _, subject := range batch.subjects {
			frame := batch.frames[subject]
			if frame == nil {
				frame = &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, plan.CarryColumns...), Rows: [][]any{}}
			}
			frames[subject] = frame
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if failedBatches == len(jobs) {
		return nil, nil, fmt.Errorf("%w: all %d factor read batches failed: %v", storageio.ErrInfra, failedBatches, firstErr)
	}
	return frames, failed, nil
}

// readBatch reads one batch with retries. A live read covers a few bars and
// must fail fast, so the whole batch shares one deadline. A recalc or warm-up
// read spans the whole lookback over a link the read workers share; it is
// bounded per page instead, so a slow but progressing read is not abandoned.
func (r *Runner) readBatch(ctx context.Context, plan Plan, start time.Time, subjects []string, pageDeadline bool) (map[string]*storageio.Frame, error) {
	retries := max(r.cfg.ReadRetries, 0)
	timeout := r.cfg.ReadTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		readCtx, cancel := ctx, context.CancelFunc(func() {})
		pageTimeout := time.Duration(0)
		if pageDeadline {
			pageTimeout = timeout
		} else {
			readCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		attemptStart := time.Now()
		frames, err := r.store.ReadWindow(readCtx, storageio.ReadRequest{
			SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID, Freq: plan.Set.Freq,
			Subjects: append([]string(nil), subjects...), Start: start, End: plan.TargetEnd,
			Columns: append([]string(nil), plan.CarryColumns...), PageTimeout: pageTimeout,
		})
		cancel()
		if err == nil {
			return frames, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A failed batch only surfaces as degraded subjects in the outcome;
		// log each attempt so the cause (timeout, Storage error) is visible.
		log.Warnf("factor_read_batch_failed set_id=%s subjects=%d first=%s attempt=%d/%d after=%s timeout=%s error=%v",
			plan.Set.SetID, len(subjects), subjects[0], attempt+1, retries+1, time.Since(attemptStart).Round(time.Millisecond), timeout, err)
	}
	return nil, lastErr
}

// RunWarmup loads the lookback window of the subjects live periods left out as
// warming, until ctx ends. It needs Config.BackgroundWarmup.
func (r *Runner) RunWarmup(ctx context.Context) {
	if r == nil || r.windows == nil || r.windows.wake == nil {
		return
	}
	for {
		plan, subjects, ok := r.windows.nextWarmup()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-r.windows.wake:
				continue
			}
		}
		if !r.warmup(ctx, plan, subjects) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(warmupRetryDelay):
			}
		}
	}
}

// WarmupStatuses reports the warm-up state of every live set seen so far.
func (r *Runner) WarmupStatuses() []WarmupStatus {
	if r == nil || r.windows == nil {
		return nil
	}
	return r.windows.statuses()
}

func (r *Runner) warmup(ctx context.Context, plan Plan, subjects []string) bool {
	defer r.windows.finishWarmup(plan, subjects)
	started := time.Now()
	reads := make(map[string]subjectRead, len(subjects))
	for _, subject := range subjects {
		reads[subject] = subjectRead{start: plan.TargetStart}
	}
	frames, failed, err := r.readSubjects(ctx, plan, reads, true)
	if err != nil {
		log.Warnf("factor_warmup_failed set_id=%s subjects=%d window=%s/%s duration=%s error=%v", plan.Set.SetID, len(subjects),
			plan.TargetStart.UTC().Format(time.RFC3339), plan.TargetEnd.UTC().Format(time.RFC3339), time.Since(started).Round(time.Millisecond), err)
		return false
	}
	r.windows.store(plan, frames)
	log.Infof("factor_warmup_done set_id=%s subjects=%d warmed=%d failed=%d window=%s/%s duration=%s", plan.Set.SetID, len(subjects), len(frames), len(failed),
		plan.TargetStart.UTC().Format(time.RFC3339), plan.TargetEnd.UTC().Format(time.RFC3339), time.Since(started).Round(time.Millisecond))
	return len(failed) == 0
}

func readWindowStart(clock periodclock.Clock, plan Plan) (time.Time, error) {
	if plan.Mode != ModeRecalc || len(plan.Factors) == 0 {
		return plan.TargetStart, nil
	}
	maxLookback := 1
	for _, factor := range plan.Factors {
		if factor.LookbackPeriods > maxLookback {
			maxLookback = factor.LookbackPeriods
		}
	}
	window, err := clock.Window(plan.TargetStart, plan.Set.Freq, maxLookback)
	if err != nil {
		return time.Time{}, fmt.Errorf("compute recalc read lookback: %w", err)
	}
	return window[0], nil
}

func keysOf[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
