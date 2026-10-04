package recalc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

type Worker struct {
	db     *store.Store
	runner Runner
	cfg    config
	serial chan struct{}
}

func NewWorker(db *store.Store, runner Runner, options ...Option) *Worker {
	return &Worker{db: db, runner: runner, cfg: defaultConfig(options), serial: make(chan struct{}, 1)}
}

func (w *Worker) RunPending(ctx context.Context) error {
	if w == nil || w.db == nil {
		return errors.New("factor recalc store is required")
	}
	jobs, err := w.db.ListRecalcJobs(ctx, store.RecalcJobFilter{Statuses: []string{store.RecalcStatusAccepted, store.RecalcStatusRunning}})
	if err != nil {
		return err
	}
	var failures []error
	for i := len(jobs) - 1; i >= 0; i-- {
		if err := w.Run(ctx, jobs[i].JobID); err != nil {
			failures = append(failures, fmt.Errorf("run recalc job %q: %w", jobs[i].JobID, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

func (w *Worker) Run(ctx context.Context, jobID string) error {
	if w == nil || w.db == nil || w.runner == nil {
		return errors.New("factor recalc store and runner are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := w.acquire(ctx); err != nil {
		return err
	}
	defer w.release()
	job, err := w.db.GetRecalcJob(ctx, jobID)
	if err != nil {
		return err
	}
	if terminal(job.Status) {
		return nil
	}
	job, err = w.db.UpdateRecalcJob(ctx, job.JobID, func(current *store.RecalcJob) error {
		if current.Status == store.RecalcStatusCancelled {
			return nil
		}
		current.Status = store.RecalcStatusRunning
		if current.ProgressTime == 0 {
			current.ProgressTime = current.StartTime
		}
		return nil
	})
	if err != nil {
		return err
	}
	if job.Status == store.RecalcStatusCancelled {
		return nil
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err = w.db.GetRecalcJob(ctx, job.JobID)
		if err != nil {
			return err
		}
		if job.Status == store.RecalcStatusCancelled {
			return nil
		}
		if job.Status != store.RecalcStatusRunning {
			return fmt.Errorf("recalc job %q is not runnable: %s", job.JobID, job.Status)
		}
		start := job.ProgressTime
		if start < job.StartTime {
			start = job.StartTime
		}
		if start >= job.EndTime {
			_, err := w.db.UpdateRecalcJob(ctx, job.JobID, func(current *store.RecalcJob) error {
				current.ProgressTime = current.EndTime
				current.Status = store.RecalcStatusSucceeded
				current.Error = ""
				return nil
			})
			return err
		}
		set, _, subjects, clock, err := w.selection(ctx, job.SetID, job.FactorIDs, job.Subjects)
		if err != nil {
			return w.fail(ctx, job.JobID, err)
		}
		if len(subjects) == 0 {
			_, err := w.db.UpdateRecalcJob(ctx, job.JobID, func(current *store.RecalcJob) error {
				current.ProgressTime = current.EndTime
				current.Status = store.RecalcStatusSucceeded
				current.Error = ""
				return nil
			})
			return err
		}
		duration, err := clock.Duration(set.Freq)
		if err != nil {
			return w.fail(ctx, job.JobID, err)
		}
		if int64(w.cfg.chunkPeriods) > math.MaxInt64/int64(duration) {
			return w.fail(ctx, job.JobID, errors.New("recalc chunk duration overflows"))
		}
		chunkEnd := time.Unix(start, 0).UTC().Add(time.Duration(w.cfg.chunkPeriods) * duration)
		end := time.Unix(job.EndTime, 0).UTC()
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		chunkStart := time.Unix(start, 0).UTC()
		outcome, err := w.runChunkWithRetry(ctx, job.JobID, job.SetID, job.FactorIDs, job.Subjects, chunkStart, chunkEnd)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			current, getErr := w.db.GetRecalcJob(ctx, job.JobID)
			if getErr == nil && current.Status == store.RecalcStatusCancelled {
				return nil
			}
			return w.fail(ctx, job.JobID, err)
		}
		job, err = w.db.UpdateRecalcJob(ctx, job.JobID, func(current *store.RecalcJob) error {
			if current.Status == store.RecalcStatusCancelled {
				return nil
			}
			current.ProgressTime = chunkEnd.Unix()
			if note := degradedNote(outcome); note != "" {
				current.Error = note
			} else if !strings.HasPrefix(current.Error, degradedPrefix) {
				current.Error = ""
			}
			if current.ProgressTime >= current.EndTime {
				current.Status = store.RecalcStatusSucceeded
			} else {
				current.Status = store.RecalcStatusRunning
			}
			return nil
		})
		if err != nil {
			return err
		}
		if job.Status == store.RecalcStatusCancelled || job.Status == store.RecalcStatusSucceeded {
			return nil
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context, setID string, factorIDs, subjects []string, period time.Time) (pipeline.Outcome, error) {
	if w == nil || w.db == nil || w.runner == nil {
		return pipeline.Outcome{}, errors.New("factor recalc store and runner are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := w.acquire(ctx); err != nil {
		return pipeline.Outcome{}, err
	}
	defer w.release()
	set, _, _, clock, err := w.selection(ctx, strings.TrimSpace(setID), factorIDs, subjects)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	start, err := clock.Align(period, set.Freq)
	if err != nil {
		return pipeline.Outcome{}, fmt.Errorf("align recalc period: %w", err)
	}
	duration, err := clock.Duration(set.Freq)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	return w.runChunk(ctx, "", set.SetID, factorIDs, subjects, start, start.Add(duration))
}

func (w *Worker) runChunk(ctx context.Context, jobID, setID string, factorIDs, subjects []string, start, end time.Time) (pipeline.Outcome, error) {
	unlock, err := w.cfg.locks.LockContext(ctx, setID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return pipeline.Outcome{}, err
	}
	if jobID != "" {
		job, err := w.db.GetRecalcJob(ctx, jobID)
		if err != nil {
			return pipeline.Outcome{}, err
		}
		if job.Status == store.RecalcStatusCancelled {
			return pipeline.Outcome{}, context.Canceled
		}
		if job.Status != store.RecalcStatusRunning {
			return pipeline.Outcome{}, fmt.Errorf("recalc job %q is not running", jobID)
		}
	}
	set, factors, selectedSubjects, _, err := w.selection(ctx, setID, factorIDs, subjects)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	columns, err := w.columns(ctx, set, factors)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	return w.runner.Run(ctx, pipeline.Plan{
		Mode: pipeline.ModeRecalc, Set: set, Factors: factors,
		TargetStart: start, TargetEnd: end,
		Expected:     append([]string(nil), selectedSubjects...),
		Available:    append([]string(nil), selectedSubjects...),
		CarryColumns: columns, WriteCarry: true,
	})
}

func (w *Worker) fail(ctx context.Context, jobID string, cause error) error {
	_, updateErr := w.db.UpdateRecalcJob(ctx, jobID, func(current *store.RecalcJob) error {
		if current.Status != store.RecalcStatusCancelled {
			current.Status = store.RecalcStatusFailed
			current.Error = cause.Error()
		}
		return nil
	})
	return errors.Join(cause, updateErr)
}

func (w *Worker) acquire(ctx context.Context) error {
	select {
	case w.serial <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) release() { <-w.serial }

func terminal(status string) bool {
	return status == store.RecalcStatusSucceeded || status == store.RecalcStatusFailed || status == store.RecalcStatusCancelled
}

const degradedPrefix = "degraded:"

// runChunkWithRetry retries a chunk only for transient Storage failures. Progress
// is committed after a successful chunk, so a final failure still leaves the job
// resumable from the last completed chunk by a new request.
func (w *Worker) runChunkWithRetry(ctx context.Context, jobID, setID string, factorIDs, subjects []string, start, end time.Time) (pipeline.Outcome, error) {
	var lastErr error
	for attempt := 1; attempt <= w.cfg.chunkRetries; attempt++ {
		outcome, err := w.runChunk(ctx, jobID, setID, factorIDs, subjects, start, end)
		if err == nil {
			return outcome, nil
		}
		lastErr = err
		if !errors.Is(err, storageio.ErrInfra) || attempt == w.cfg.chunkRetries {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * w.cfg.retryBackoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return pipeline.Outcome{}, ctx.Err()
		case <-timer.C:
		}
	}
	return pipeline.Outcome{}, lastErr
}

// degradedNote summarises partial failures of a chunk; the job still succeeds
// because the factor values of unaffected subjects were written.
func degradedNote(outcome pipeline.Outcome) string {
	if outcome.Status != "degraded" {
		return ""
	}
	var factors []string
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			factors = append(factors, factor.FactorID)
		}
	}
	note := fmt.Sprintf("%s failed_subjects=%d", degradedPrefix, len(outcome.FailedSubjects))
	if len(factors) > 0 {
		note += " factors=" + strings.Join(factors, ",")
	}
	return note
}
