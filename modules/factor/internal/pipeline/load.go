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
	readStart, err := readWindowStart(r.clock, plan)
	if err != nil {
		return LoadResult{}, err
	}
	live := plan.Mode == ModeLive && r.windows != nil
	starts := map[string]time.Time{}
	var prefixes map[string][][]any
	if live {
		starts, prefixes = r.windows.readStarts(plan, subjects)
	}
	batchSize := r.cfg.ReadBatchSubjects
	if batchSize < 1 {
		batchSize = 100
	}
	// Subjects sharing a read start share batches, so a warm live set reads
	// only its recent bars while a cold subject still reads the whole window.
	type readJob struct {
		start    time.Time
		subjects []string
	}
	groups := make(map[time.Time][]string)
	var groupOrder []time.Time
	for _, subject := range subjects {
		start, ok := starts[subject]
		if !ok {
			start = readStart
		}
		if _, seen := groups[start]; !seen {
			groupOrder = append(groupOrder, start)
		}
		groups[start] = append(groups[start], subject)
	}
	var batches []readJob
	for _, start := range groupOrder {
		group := groups[start]
		for from := 0; from < len(group); from += batchSize {
			to := from + batchSize
			if to > len(group) {
				to = len(group)
			}
			batches = append(batches, readJob{start: start, subjects: append([]string(nil), group[from:to]...)})
		}
	}
	workers := r.cfg.ReadWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > len(batches) {
		workers = len(batches)
	}
	type batchResult struct {
		subjects []string
		frames   map[string]*storageio.Frame
		err      error
	}
	jobs := make(chan readJob)
	results := make(chan batchResult, len(batches))
	var workersDone sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for job := range jobs {
				frames, err := r.readBatch(ctx, plan, job.start, job.subjects)
				results <- batchResult{subjects: job.subjects, frames: frames, err: err}
			}
		}()
	}
	go func() {
		for _, batch := range batches {
			select {
			case jobs <- batch:
			case <-ctx.Done():
				close(jobs)
				workersDone.Wait()
				close(results)
				return
			}
		}
		close(jobs)
		workersDone.Wait()
		close(results)
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
				failedSet[subject] = struct{}{}
			}
			continue
		}
		for _, subject := range batch.subjects {
			frame := batch.frames[subject]
			if frame == nil {
				frame = &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, plan.CarryColumns...), Rows: [][]any{}}
			}
			if prefix := prefixes[subject]; len(prefix) > 0 {
				frame.Rows = append(append(make([][]any, 0, len(prefix)+len(frame.Rows)), prefix...), frame.Rows...)
			}
			result.Frames[subject] = frame
		}
	}
	if err := ctx.Err(); err != nil {
		return LoadResult{}, err
	}
	if failedBatches == len(batches) {
		return LoadResult{}, fmt.Errorf("%w: all %d factor read batches failed: %v", storageio.ErrInfra, failedBatches, firstErr)
	}
	if live {
		r.windows.store(plan, result.Frames, failedSet)
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

func (r *Runner) readBatch(ctx context.Context, plan Plan, start time.Time, subjects []string) (map[string]*storageio.Frame, error) {
	retries := r.cfg.ReadRetries
	if retries < 0 {
		retries = 0
	}
	timeout := r.cfg.ReadTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A live period reads a few pages and must fail fast, so the whole
		// batch shares one deadline. A backfill batch reads tens of pages over
		// a link the read workers share; it is bounded per page instead, so a
		// slow but progressing read is not abandoned mid-way and retried.
		readCtx, cancel := ctx, context.CancelFunc(func() {})
		pageTimeout := time.Duration(0)
		if plan.Mode == ModeRecalc {
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
