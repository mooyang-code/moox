package recalc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

type Worker struct {
	db       *store.Store
	runner   Runner
	cfg      config
	executor *Executor
	serial   chan struct{}
}

func NewWorker(db *store.Store, runner Runner, options ...Option) *Worker {
	return &Worker{db: db, runner: runner, cfg: defaultConfig(options), executor: NewExecutor(runner, options...), serial: make(chan struct{}, 1)}
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
	window := Window{
		Start: time.Unix(job.StartTime, 0).UTC(), End: time.Unix(job.EndTime, 0).UTC(),
		Progress: time.Unix(job.ProgressTime, 0).UTC(),
	}
	err = w.executor.Run(ctx, window, w.jobSelection(job), w.jobProgress(job.JobID))
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	current, getErr := w.db.GetRecalcJob(ctx, job.JobID)
	if getErr == nil && current.Status == store.RecalcStatusCancelled {
		return nil
	}
	return w.fail(ctx, job.JobID, err)
}

// jobSelection re-reads the job and the set membership before every chunk, so
// a cancellation or a disabled member takes effect at the next chunk boundary.
func (w *Worker) jobSelection(job store.RecalcJob) SelectionSource {
	return func(ctx context.Context) (Selection, error) {
		current, err := w.db.GetRecalcJob(ctx, job.JobID)
		if err != nil {
			return Selection{}, err
		}
		if current.Status == store.RecalcStatusCancelled {
			return Selection{}, context.Canceled
		}
		if current.Status != store.RecalcStatusRunning {
			return Selection{}, fmt.Errorf("recalc job %q is not running", job.JobID)
		}
		return w.load(ctx, job.SetID, job.FactorIDs, job.Subjects)
	}
}

func (w *Worker) jobProgress(jobID string) ProgressFunc {
	return func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (bool, error) {
		job, err := w.db.UpdateRecalcJob(ctx, jobID, func(current *store.RecalcJob) error {
			if current.Status == store.RecalcStatusCancelled {
				return nil
			}
			current.ProgressTime = progress.Unix()
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
			return true, err
		}
		return job.Status == store.RecalcStatusCancelled || job.Status == store.RecalcStatusSucceeded, nil
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
	selection, err := w.load(ctx, strings.TrimSpace(setID), factorIDs, subjects)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	clock, err := w.clockFor(selection.Set.SpaceID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	start, err := clock.Align(period, selection.Set.Freq)
	if err != nil {
		return pipeline.Outcome{}, fmt.Errorf("align recalc period: %w", err)
	}
	duration, err := clock.Duration(selection.Set.Freq)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	return w.executor.RunChunk(ctx, selection, start, start.Add(duration))
}

// load resolves the chunk input from the store: enabled set, selected enabled
// members, scoped subjects and the source columns carried into the result.
func (w *Worker) load(ctx context.Context, setID string, factorIDs, subjects []string) (Selection, error) {
	set, factors, selectedSubjects, _, err := w.selection(ctx, setID, factorIDs, subjects)
	if err != nil {
		return Selection{}, err
	}
	columns, err := w.columns(ctx, set, factors)
	if err != nil {
		return Selection{}, err
	}
	return Selection{Set: set, Factors: factors, Subjects: selectedSubjects, Columns: columns}, nil
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
