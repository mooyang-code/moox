package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
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
	JobID           string    `json:"job_id"`
	RequestID       string    `json:"request_id"`
	SetID           string    `json:"set_id"`
	FactorIDs       []string  `json:"factor_ids"`
	Subjects        []string  `json:"subjects"`
	FactorsOmitted  bool      `json:"-"`
	SubjectsOmitted bool      `json:"-"`
	StartTime       int64     `json:"start_time"`
	EndTime         int64     `json:"end_time"`
	Status          string    `json:"status"`
	ProgressTime    int64     `json:"progress_time"`
	Error           string    `json:"error"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type recalcJobRow struct {
	JobID           string    `gorm:"column:c_job_id"`
	RequestID       string    `gorm:"column:c_request_id"`
	SetID           string    `gorm:"column:c_set_id"`
	FactorIDsJSON   string    `gorm:"column:c_factor_ids_json"`
	SubjectsJSON    string    `gorm:"column:c_subjects_json"`
	FactorsOmitted  bool      `gorm:"column:c_factors_omitted"`
	SubjectsOmitted bool      `gorm:"column:c_subjects_omitted"`
	StartTime       int64     `gorm:"column:c_start_time"`
	EndTime         int64     `gorm:"column:c_end_time"`
	Status          string    `gorm:"column:c_status"`
	ProgressTime    int64     `gorm:"column:c_progress_time"`
	Error           string    `gorm:"column:c_error"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
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
		FactorIDs: factorIDs, Subjects: subjects,
		FactorsOmitted: r.FactorsOmitted, SubjectsOmitted: r.SubjectsOmitted, StartTime: r.StartTime,
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
	job, factorIDsJSON, subjectsJSON, err := normalizeRecalcJob(job)
	if err != nil {
		return RecalcJob{}, err
	}
	var stored RecalcJob
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		var created bool
		stored, created, err = insertRecalcJob(tx, job, factorIDsJSON, subjectsJSON)
		if err == nil && !created && !sameRecalcRequest(stored, job) {
			err = fmt.Errorf("%w: request_id %q belongs to a different recalc request", ErrConflict, job.RequestID)
		}
		return err
	})
	if err != nil {
		return RecalcJob{}, err
	}
	return stored, nil
}

// EnableFactorWithRecalcJob atomically accepts a factor's initial backfill and
// makes the factor visible to live trigger selection.
func (s *Store) EnableFactorWithRecalcJob(ctx context.Context, factorID, expectedStatus string, job RecalcJob) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	factorID = strings.TrimSpace(factorID)
	if factorID == "" || expectedStatus != domain.FactorStatusDisabled || job.Status != RecalcStatusAccepted {
		return RecalcJob{}, fmt.Errorf("factor activation request is invalid")
	}
	job, factorIDsJSON, subjectsJSON, err := normalizeRecalcJob(job)
	if err != nil {
		return RecalcJob{}, err
	}
	if !containsString(job.FactorIDs, factorID) {
		return RecalcJob{}, fmt.Errorf("factor activation job does not include factor %q", factorID)
	}
	var stored RecalcJob
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var created bool
		var insertErr error
		stored, created, insertErr = insertRecalcJob(tx, job, factorIDsJSON, subjectsJSON)
		if insertErr != nil {
			return insertErr
		}
		if !created && !sameRecalcRequest(stored, job) {
			return fmt.Errorf("%w: request_id %q belongs to a different recalc request", ErrConflict, job.RequestID)
		}
		result := tx.Exec(`
			UPDATE t_factor_defs SET c_status = ?, c_mtime = ?
			WHERE c_factor_id = ? AND c_set_id = ? AND c_status = ?
		`, domain.FactorStatusEnabled, time.Now().UTC(), factorID, job.SetID, expectedStatus)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}
		var current struct {
			Status string `gorm:"column:c_status"`
			SetID  string `gorm:"column:c_set_id"`
		}
		lookup := tx.Raw(`SELECT c_status, c_set_id FROM t_factor_defs WHERE c_factor_id = ?`, factorID).Scan(&current)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if !created && current.SetID == job.SetID && current.Status == domain.FactorStatusEnabled {
			return nil
		}
		return fmt.Errorf("%w: factor %q status is %q, expected %q", ErrConflict, factorID, current.Status, expectedStatus)
	})
	if err != nil {
		return RecalcJob{}, err
	}
	return stored, nil
}

func normalizeRecalcJob(job RecalcJob) (RecalcJob, string, string, error) {
	job.RequestID = strings.TrimSpace(job.RequestID)
	job.SetID = strings.TrimSpace(job.SetID)
	if job.RequestID == "" || job.SetID == "" || job.EndTime <= job.StartTime {
		return RecalcJob{}, "", "", fmt.Errorf("recalc job identity or time range is invalid")
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
		return RecalcJob{}, "", "", err
	}
	subjectsJSON, err := marshalStringSlice(job.Subjects)
	if err != nil {
		return RecalcJob{}, "", "", err
	}
	return job, factorIDsJSON, subjectsJSON, nil
}

func insertRecalcJob(tx *gorm.DB, job RecalcJob, factorIDsJSON, subjectsJSON string) (RecalcJob, bool, error) {
	now := time.Now().UTC()
	result := tx.Exec(`
		INSERT INTO t_factor_recalc_jobs
		(c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
		 c_factors_omitted, c_subjects_omitted,
		 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_request_id) DO NOTHING
	`, job.JobID, job.RequestID, job.SetID, factorIDsJSON, subjectsJSON,
		job.FactorsOmitted, job.SubjectsOmitted,
		job.StartTime, job.EndTime, job.Status, job.ProgressTime, job.Error, now, now)
	if result.Error != nil {
		return RecalcJob{}, false, result.Error
	}
	var existing recalcJobRow
	lookup := tx.Raw(`
		SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
		 c_factors_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status,
		 c_progress_time, c_error, c_ctime, c_mtime
		FROM t_factor_recalc_jobs WHERE c_request_id = ?
	`, job.RequestID).Scan(&existing)
	if lookup.Error != nil {
		return RecalcJob{}, false, lookup.Error
	}
	if lookup.RowsAffected > 0 {
		stored, err := existing.job()
		return stored, result.RowsAffected > 0, err
	}
	return RecalcJob{}, false, fmt.Errorf("%w: recalc job id already belongs to another request", ErrConflict)
}

func sameRecalcRequest(left, right RecalcJob) bool {
	if left.RequestID != right.RequestID || left.SetID != right.SetID ||
		left.StartTime != right.StartTime || left.EndTime != right.EndTime ||
		left.FactorsOmitted != right.FactorsOmitted || left.SubjectsOmitted != right.SubjectsOmitted {
		return false
	}
	return (left.FactorsOmitted || sameStringValues(left.FactorIDs, right.FactorIDs)) &&
		(left.SubjectsOmitted || sameStringValues(left.Subjects, right.Subjects))
}

func sameStringValues(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]string(nil), left...)
	right = append([]string(nil), right...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
			 c_factors_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
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
			 c_factors_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
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

// FindRecalcJobByRequestID returns the accepted job for an idempotency key.
func (s *Store) FindRecalcJobByRequestID(ctx context.Context, requestID string) (RecalcJob, bool, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, false, fmt.Errorf("factor database is not open")
	}
	row, err := s.getRecalcJobRow(ctx, "c_request_id", strings.TrimSpace(requestID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RecalcJob{}, false, nil
	}
	if err != nil {
		return RecalcJob{}, false, err
	}
	job, err := row.job()
	return job, err == nil, err
}

// RecalcJobFilter narrows ListRecalcJobs; zero fields match everything.
type RecalcJobFilter struct {
	SetID    string
	Statuses []string
}

// ListRecalcJobs returns jobs newest first, optionally restricted to one factor set and statuses.
func (s *Store) ListRecalcJobs(ctx context.Context, filter RecalcJobFilter) ([]RecalcJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT c_job_id, c_request_id, c_set_id, c_factor_ids_json, c_subjects_json,
	 c_factors_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
	 FROM t_factor_recalc_jobs`
	var conditions []string
	var args []any
	if setID := strings.TrimSpace(filter.SetID); setID != "" {
		conditions = append(conditions, "c_set_id = ?")
		args = append(args, setID)
	}
	var statuses []string
	for _, status := range filter.Statuses {
		if status = strings.TrimSpace(status); status != "" {
			statuses = append(statuses, status)
		}
	}
	if len(statuses) > 0 {
		conditions = append(conditions, "c_status IN ("+strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")+")")
		for _, status := range statuses {
			args = append(args, status)
		}
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
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
		 c_factors_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime
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
