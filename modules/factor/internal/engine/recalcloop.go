package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"trpc.group/trpc-go/trpc-go/log"
)

// errReportFailed marks a job abandoned because its progress could not be
// reported; the job lease expires and the job resumes from its last progress.
var errReportFailed = errors.New("recalc progress report failed")

const (
	jobStatusRunning   = "running"
	jobStatusSucceeded = "succeeded"
	jobStatusFailed    = "failed"
)

// JobClient is the manager surface of the recalc loop.
type JobClient interface {
	PullRecalcJob(ctx context.Context) (PulledJob, bool, error)
	ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress time.Time, status, errText string) (string, error)
}

// SourceDataset reads the source dataset facts a recalc chunk needs.
type SourceDataset interface {
	DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

type ChunkExecutor interface {
	Run(ctx context.Context, window recalcexec.Window, source recalcexec.SelectionSource, report recalcexec.ProgressFunc) error
}

// recalcLoop pulls leased jobs from the manager and runs them chunk by chunk,
// reporting every chunk (which also renews the job lease).
type recalcLoop struct {
	client   JobClient
	executor ChunkExecutor
	source   SourceDataset
	active   func() bool
	poll     time.Duration
}

// RunOnce pulls and runs at most one job; it reports whether a job was found.
func (l *recalcLoop) RunOnce(ctx context.Context) (bool, error) {
	if l.active != nil && !l.active() {
		return false, nil
	}
	job, found, err := l.client.PullRecalcJob(ctx)
	if err != nil || !found {
		return false, err
	}
	log.InfoContextf(ctx, "factor_recalc_start job_id=%s set_id=%s start=%s end=%s progress=%s", job.JobID, job.Set.Set.SetID,
		job.Window.Start.Format(time.RFC3339), job.Window.End.Format(time.RFC3339), job.Window.Progress.Format(time.RFC3339))
	err = l.runJob(ctx, job)
	switch {
	case err == nil:
		log.InfoContextf(ctx, "factor_recalc_done job_id=%s", job.JobID)
	case errors.Is(err, ErrLeaseConflict), errors.Is(err, errReportFailed):
		log.WarnContextf(ctx, "factor_recalc_abandoned job_id=%s error=%v", job.JobID, err)
	case ctx.Err() != nil:
		// Shutdown: the lease expires and the job resumes from its progress.
	default:
		log.ErrorContextf(ctx, "factor_recalc_failed job_id=%s error=%v", job.JobID, err)
		if _, reportErr := l.client.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, job.Window.Progress, jobStatusFailed, err.Error()); reportErr != nil {
			log.ErrorContextf(ctx, "factor_recalc_report_failed job_id=%s error=%v", job.JobID, reportErr)
		}
	}
	return true, nil
}

func (l *recalcLoop) runJob(ctx context.Context, job PulledJob) error {
	set := job.Set
	lastNote := ""
	report := func(ctx context.Context, progress time.Time, outcome pipeline.Outcome) (bool, error) {
		status := jobStatusRunning
		if !progress.Before(job.Window.End) {
			status = jobStatusSucceeded
		}
		if note := recalcexec.DegradedNote(outcome); note != "" {
			lastNote = note
		}
		current, err := l.client.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, progress, status, lastNote)
		if err != nil {
			return true, fmt.Errorf("%w: %w", errReportFailed, err)
		}
		job.Window.Progress = progress
		return current != jobStatusRunning, nil
	}
	source := func(ctx context.Context) (recalcexec.Selection, error) {
		subjects, err := l.subjects(ctx, set, job.Subjects)
		if err != nil {
			return recalcexec.Selection{}, err
		}
		columns, err := l.source.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
		if err != nil {
			return recalcexec.Selection{}, fmt.Errorf("list source dataset columns: %w", err)
		}
		return recalcexec.Selection{Set: set.Set, Factors: set.Factors, Subjects: subjects, Columns: columns}, nil
	}
	return l.executor.Run(ctx, job.Window, source, report)
}

// subjects resolves an empty job selection the way the manager accepted it:
// an include set uses its own subjects, an all set the source dataset's.
func (l *recalcLoop) subjects(ctx context.Context, set domain.EngineSet, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return requested, nil
	}
	if set.Set.SubjectMode == domain.SubjectModeInclude {
		return set.Set.Subjects, nil
	}
	subjects, err := l.source.ListDatasetSubjects(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("list source dataset subjects: %w", err)
	}
	return subjects, nil
}

func (l *recalcLoop) Run(ctx context.Context) {
	for ctx.Err() == nil {
		found, err := l.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			log.WarnContextf(ctx, "factor_recalc_pull_failed error=%v", err)
		}
		if found {
			continue
		}
		timer := time.NewTimer(l.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
