package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
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
	batchSize := r.cfg.ReadBatchSubjects
	if batchSize < 1 {
		batchSize = 100
	}
	batches := make([][]string, 0, (len(subjects)+batchSize-1)/batchSize)
	for start := 0; start < len(subjects); start += batchSize {
		end := start + batchSize
		if end > len(subjects) {
			end = len(subjects)
		}
		batches = append(batches, append([]string(nil), subjects[start:end]...))
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
	jobs := make(chan []string)
	results := make(chan batchResult, len(batches))
	var workersDone sync.WaitGroup
	for i := 0; i < workers; i++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for batch := range jobs {
				frames, err := r.readBatch(ctx, plan, batch)
				results <- batchResult{subjects: batch, frames: frames, err: err}
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
			result.Frames[subject] = frame
		}
	}
	if err := ctx.Err(); err != nil {
		return LoadResult{}, err
	}
	if failedBatches == len(batches) {
		return LoadResult{}, fmt.Errorf("%w: all %d factor read batches failed: %v", storageio.ErrInfra, failedBatches, firstErr)
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

func (r *Runner) readBatch(ctx context.Context, plan Plan, subjects []string) (map[string]*storageio.Frame, error) {
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
		readCtx, cancel := context.WithTimeout(ctx, timeout)
		frames, err := r.store.ReadWindow(readCtx, storageio.ReadRequest{
			SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID, Freq: plan.Set.Freq,
			Subjects: append([]string(nil), subjects...), Start: plan.TargetStart, End: plan.TargetEnd,
			Columns: append([]string(nil), plan.CarryColumns...),
		})
		cancel()
		if err == nil {
			return frames, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}
