package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	RecalcStatusAccepted  = "accepted"
	RecalcStatusRunning   = "running"
	RecalcStatusSucceeded = "succeeded"
	RecalcStatusFailed    = "failed"
	RecalcStatusCancelled = "cancelled"
)

// RecalcJob is the durable state needed to accept, resume, inspect, or cancel
// one asynchronous factor-set recalculation.
type RecalcJob struct {
	JobID        string    `json:"job_id"`
	RequestID    string    `json:"request_id"`
	SetID        string    `json:"set_id"`
	FactorIDs    []string  `json:"factor_ids"`
	Subjects     []string  `json:"subjects"`
	StartTime    int64     `json:"start_time"`
	EndTime      int64     `json:"end_time"`
	Status       string    `json:"status"`
	ProgressTime int64     `json:"progress_time"`
	Error        string    `json:"error"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type recalcJobRow struct {
	JobID         string    `gorm:"column:c_job_id"`
	RequestID     string    `gorm:"column:c_request_id"`
	SetID         string    `gorm:"column:c_set_id"`
	FactorIDsJSON string    `gorm:"column:c_factor_ids_json"`
	SubjectsJSON  string    `gorm:"column:c_subjects_json"`
	StartTime     int64     `gorm:"column:c_start_time"`
	EndTime       int64     `gorm:"column:c_end_time"`
	Status        string    `gorm:"column:c_status"`
	ProgressTime  int64     `gorm:"column:c_progress_time"`
	Error         string    `gorm:"column:c_error"`
	CreatedAt     time.Time `gorm:"column:c_ctime"`
	UpdatedAt     time.Time `gorm:"column:c_mtime"`
}

func (r recalcJobRow) job() (RecalcJob, error) {
	factorIDs, err := unmarshalStringSlice(r.FactorIDsJSON)
	if err != nil {
		return RecalcJob{}, fmt.Errorf("decode recalc factor ids: %w", err)
	}
	subjects, err := unmarshalStringSlice(r.SubjectsJSON)
	if err != nil {
		return RecalcJob{}, fmt.Errorf("decode recalc subjects: %w", err)
	}
	return RecalcJob{
		JobID: r.JobID, RequestID: r.RequestID, SetID: r.SetID,
		FactorIDs: factorIDs, Subjects: subjects, StartTime: r.StartTime,
		EndTime: r.EndTime, Status: r.Status, ProgressTime: r.ProgressTime,
		Error: r.Error, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateRecalcJob inserts a job or returns the existing job for the same
// request id, making request retries idempotent.
func (s *Store) CreateRecalcJob(ctx context.Context, job RecalcJob) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	job.RequestID = strings.TrimSpace(job.RequestID)
	job.SetID = strings.TrimSpace(job.SetID)
	if job.RequestID == "" || job.SetID == "" || job.EndTime <= job.StartTime {
		return RecalcJob{}, fmt.Errorf("recalc job identity or time range is invalid")
	}
	if job.JobID == "" {
		job.JobID = job.RequestID
	}
	if job.Status == "" {
		job.Status = RecalcStatusAccepted
	}
	if job.FactorIDs == nil {
		job.FactorIDs = []string{}
	}
	if job.Subjects == nil {
		job.Subjects = []string{}
	}
	factorIDsJSON, err := marshalStringSlice(job.FactorIDs)
	if err != nil {
		return RecalcJob{}, err
	}
	subjectsJSON, err := marshalStringSlice(job.Subjects)
	if err != nil {
		return RecalcJob{}, err
	}
	now := time.Now().UTC()
	var stored RecalcJob
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing recalcJobRow
		lookup := tx.Raw(`
			SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
			 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
			FROM t_factor_recalc_jobs WHERE c_request_id = ?
		`, job.RequestID).Scan(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected > 0 {
			var decodeErr error
			stored, decodeErr = existing.job()
			return decodeErr
		}
		if err := tx.Exec(`
			INSERT INTO t_factor_recalc_jobs
			(c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
			 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, job.JobID, job.RequestID, job.SetID, factorIDsJSON, subjectsJSON,
			job.StartTime, job.EndTime, job.Status, job.ProgressTime, job.Error, now, now).Error; err != nil {
			if isUniqueConstraint(err) {
				return fmt.Errorf("%w: recalc job id already exists", ErrConflict)
			}
			return err
		}
		stored = job
		stored.CreatedAt = now
		stored.UpdatedAt = now
		return nil
	})
	if err != nil {
		return RecalcJob{}, err
	}
	return stored, nil
}

// UpdateRecalcJob atomically applies a mutation, retaining monotonic progress
// and preventing cancelled jobs from being restarted.
func (s *Store) UpdateRecalcJob(ctx context.Context, jobID string, mutate func(*RecalcJob) error) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	if mutate == nil {
		return RecalcJob{}, fmt.Errorf("recalc job mutation is required")
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return RecalcJob{}, fmt.Errorf("recalc job id is required")
	}
	var updated RecalcJob
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row recalcJobRow
		result := tx.Raw(`
			SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
			 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
			FROM t_factor_recalc_jobs WHERE c_job_id = ?
		`, jobID).Scan(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		current, err := row.job()
		if err != nil {
			return err
		}
		candidate := current
		if err := mutate(&candidate); err != nil {
			return err
		}
		if candidate.ProgressTime < current.ProgressTime {
			candidate.ProgressTime = current.ProgressTime
		}
		if current.Status == RecalcStatusCancelled {
			candidate.Status = RecalcStatusCancelled
			candidate.ProgressTime = current.ProgressTime
		}
		if current.Status == RecalcStatusSucceeded || current.Status == RecalcStatusFailed {
			candidate.Status = current.Status
			candidate.ProgressTime = current.ProgressTime
		}
		result = tx.Exec(`
			UPDATE t_factor_recalc_jobs SET c_status = ?, c_progress_time = ?, c_error = ?, c_mtime = ?
			WHERE c_job_id = ?
		`, candidate.Status, candidate.ProgressTime, candidate.Error, time.Now().UTC(), jobID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var fresh recalcJobRow
		result = tx.Raw(`
			SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
			 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
			FROM t_factor_recalc_jobs WHERE c_job_id = ?
		`, jobID).Scan(&fresh)
		if result.Error != nil {
			return result.Error
		}
		updated, err = fresh.job()
		return err
	})
	return updated, err
}

// GetRecalcJob returns one job by id.
func (s *Store) GetRecalcJob(ctx context.Context, jobID string) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	row, err := s.getRecalcJobRow(ctx, "c_job_id", strings.TrimSpace(jobID))
	if err != nil {
		return RecalcJob{}, err
	}
	return row.job()
}

// ListRecalcJobs returns all jobs or filters by one or more statuses.
func (s *Store) ListRecalcJobs(ctx context.Context, statuses ...string) ([]RecalcJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
	 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
	 FROM t_factor_recalc_jobs`
	filtered := make([]string, 0, len(statuses))
	for _, status := range statuses {
		if status = strings.TrimSpace(status); status != "" {
			filtered = append(filtered, status)
		}
	}
	var args []any
	if len(filtered) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(filtered)), ",")
		query += " WHERE c_status IN (" + placeholders + ")"
		for _, status := range filtered {
			args = append(args, status)
		}
	}
	query += " ORDER BY c_ctime DESC, c_job_id"
	var rows []recalcJobRow
	if err := s.db.WithContext(ctx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]RecalcJob, 0, len(rows))
	for _, row := range rows {
		job, err := row.job()
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (s *Store) getRecalcJobRow(ctx context.Context, key, value string) (recalcJobRow, error) {
	if key != "c_job_id" && key != "c_request_id" {
		return recalcJobRow{}, fmt.Errorf("unsupported recalc job lookup")
	}
	var row recalcJobRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
		 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
		FROM t_factor_recalc_jobs WHERE `+key+` = ?
	`, value).Scan(&row)
	if result.Error != nil {
		return recalcJobRow{}, result.Error
	}
	if result.RowsAffected == 0 {
		return recalcJobRow{}, gorm.ErrRecordNotFound
	}
	return row, nil
}
