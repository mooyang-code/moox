package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
// one asynchronous factor-task recalculation.
type RecalcJob struct {
	JobID           string   `json:"job_id"`
	RequestID       string   `json:"request_id"`
	TaskID          string   `json:"task_id"`
	Members         []string `json:"members"`
	Subjects        []string `json:"subjects"`
	MembersOmitted  bool     `json:"-"`
	SubjectsOmitted bool     `json:"-"`
	StartTime       int64    `json:"start_time"`
	EndTime         int64    `json:"end_time"`
	Status          string   `json:"status"`
	ProgressTime    int64    `json:"progress_time"`
	Error           string   `json:"error"`
	// DefaultFilled 是按默认参数新补的期次（成员键 → 闭区间 [起始秒, 结束秒] 列表，相邻期次已合并）：
	// 原行不存在、或原行的 __params 里没有这个成员，只能用默认参数算出来，与当时的实时结果不保证一致。
	DefaultFilled  map[string][]TimeRange `json:"default_filled,omitempty"`
	EngineID       string                 `json:"engine_id"`
	LeaseToken     string                 `json:"-"`
	LeaseExpiresAt int64                  `json:"-"`
	CreatedAt      time.Time              `json:"created_at"`
	UpdatedAt      time.Time              `json:"updated_at"`
}

// TimeRange 是 Unix 秒的闭区间。
type TimeRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// MergeTimeRanges 把闭区间按起点排序，并合并重叠或首尾相接（间隔不超过 step 秒）的区间。
func MergeTimeRanges(ranges []TimeRange, step int64) []TimeRange {
	if len(ranges) == 0 {
		return nil
	}
	sorted := append([]TimeRange(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	merged := []TimeRange{sorted[0]}
	for _, next := range sorted[1:] {
		last := &merged[len(merged)-1]
		if next.Start <= last.End+step {
			last.End = max(last.End, next.End)
			continue
		}
		merged = append(merged, next)
	}
	return merged
}

func encodeDefaultFilled(filled map[string][]TimeRange) (string, error) {
	pairs := make(map[string][][2]int64, len(filled))
	for key, ranges := range filled {
		for _, r := range ranges {
			pairs[key] = append(pairs[key], [2]int64{r.Start, r.End})
		}
	}
	raw, err := json.Marshal(pairs)
	return string(raw), err
}

func decodeDefaultFilled(raw string) (map[string][]TimeRange, error) {
	var pairs map[string][][2]int64
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		return nil, err
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	filled := make(map[string][]TimeRange, len(pairs))
	for key, list := range pairs {
		for _, pair := range list {
			filled[key] = append(filled[key], TimeRange{Start: pair[0], End: pair[1]})
		}
	}
	return filled, nil
}

// recalcJobColumns is the column list every recalc job read selects, in the
// order of recalcJobRow.
const recalcJobColumns = `c_job_id, c_request_id, c_task_id, c_members_json, c_subjects_json,
	 c_members_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status, c_progress_time, c_error,
	 c_default_filled_json, c_engine_id, c_lease_token, c_lease_expires_at, c_ctime, c_mtime`

type recalcJobRow struct {
	JobID           string    `gorm:"column:c_job_id"`
	RequestID       string    `gorm:"column:c_request_id"`
	TaskID          string    `gorm:"column:c_task_id"`
	MembersJSON     string    `gorm:"column:c_members_json"`
	SubjectsJSON    string    `gorm:"column:c_subjects_json"`
	MembersOmitted  bool      `gorm:"column:c_members_omitted"`
	SubjectsOmitted bool      `gorm:"column:c_subjects_omitted"`
	StartTime       int64     `gorm:"column:c_start_time"`
	EndTime         int64     `gorm:"column:c_end_time"`
	Status          string    `gorm:"column:c_status"`
	ProgressTime    int64     `gorm:"column:c_progress_time"`
	Error           string    `gorm:"column:c_error"`
	DefaultFilled   string    `gorm:"column:c_default_filled_json"`
	EngineID        string    `gorm:"column:c_engine_id"`
	LeaseToken      string    `gorm:"column:c_lease_token"`
	LeaseExpiresAt  int64     `gorm:"column:c_lease_expires_at"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
}

func (r recalcJobRow) job() (RecalcJob, error) {
	members, err := unmarshalStringSlice(r.MembersJSON)
	if err != nil {
		return RecalcJob{}, fmt.Errorf("decode recalc members: %w", err)
	}
	subjects, err := unmarshalStringSlice(r.SubjectsJSON)
	if err != nil {
		return RecalcJob{}, fmt.Errorf("decode recalc subjects: %w", err)
	}
	filled, err := decodeDefaultFilled(r.DefaultFilled)
	if err != nil {
		return RecalcJob{}, fmt.Errorf("decode recalc default filled: %w", err)
	}
	return RecalcJob{
		JobID: r.JobID, RequestID: r.RequestID, TaskID: r.TaskID,
		Members: members, Subjects: subjects,
		MembersOmitted: r.MembersOmitted, SubjectsOmitted: r.SubjectsOmitted, StartTime: r.StartTime,
		EndTime: r.EndTime, Status: r.Status, ProgressTime: r.ProgressTime,
		Error: r.Error, DefaultFilled: filled, EngineID: r.EngineID, LeaseToken: r.LeaseToken, LeaseExpiresAt: r.LeaseExpiresAt,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateRecalcJob inserts a job or returns the existing job for the same
// request id, making request retries idempotent.
func (s *Store) CreateRecalcJob(ctx context.Context, job RecalcJob) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	job, membersJSON, subjectsJSON, err := normalizeRecalcJob(job)
	if err != nil {
		return RecalcJob{}, err
	}
	var stored RecalcJob
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		var created bool
		stored, created, err = insertRecalcJob(tx, job, membersJSON, subjectsJSON)
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

func normalizeRecalcJob(job RecalcJob) (RecalcJob, string, string, error) {
	job.RequestID = strings.TrimSpace(job.RequestID)
	job.TaskID = strings.TrimSpace(job.TaskID)
	if job.RequestID == "" || job.TaskID == "" || job.EndTime <= job.StartTime {
		return RecalcJob{}, "", "", fmt.Errorf("recalc job identity or time range is invalid")
	}
	if job.JobID == "" {
		job.JobID = job.RequestID
	}
	if job.Status == "" {
		job.Status = RecalcStatusAccepted
	}
	if job.Members == nil {
		job.Members = []string{}
	}
	if job.Subjects == nil {
		job.Subjects = []string{}
	}
	membersJSON, err := marshalStringSlice(job.Members)
	if err != nil {
		return RecalcJob{}, "", "", err
	}
	subjectsJSON, err := marshalStringSlice(job.Subjects)
	if err != nil {
		return RecalcJob{}, "", "", err
	}
	return job, membersJSON, subjectsJSON, nil
}

func insertRecalcJob(tx *gorm.DB, job RecalcJob, membersJSON, subjectsJSON string) (RecalcJob, bool, error) {
	now := time.Now().UTC()
	result := tx.Exec(`
		INSERT INTO t_factor_recalc_jobs
		(c_job_id, c_request_id, c_task_id, c_members_json, c_subjects_json,
		 c_members_omitted, c_subjects_omitted,
		 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_request_id) DO NOTHING
	`, job.JobID, job.RequestID, job.TaskID, membersJSON, subjectsJSON,
		job.MembersOmitted, job.SubjectsOmitted,
		job.StartTime, job.EndTime, job.Status, job.ProgressTime, job.Error, now, now)
	if result.Error != nil {
		return RecalcJob{}, false, result.Error
	}
	var existing recalcJobRow
	lookup := tx.Raw(`
		SELECT `+recalcJobColumns+`
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
	if left.RequestID != right.RequestID || left.TaskID != right.TaskID ||
		left.StartTime != right.StartTime || left.EndTime != right.EndTime ||
		left.MembersOmitted != right.MembersOmitted || left.SubjectsOmitted != right.SubjectsOmitted {
		return false
	}
	return (left.MembersOmitted || sameStringValues(left.Members, right.Members)) &&
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
			SELECT `+recalcJobColumns+`
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
			SELECT `+recalcJobColumns+`
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
	TaskID   string
	Statuses []string
}

// ListRecalcJobs returns jobs newest first, optionally restricted to one factor task and statuses.
func (s *Store) ListRecalcJobs(ctx context.Context, filter RecalcJobFilter) ([]RecalcJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT ` + recalcJobColumns + `
	 FROM t_factor_recalc_jobs`
	var conditions []string
	var args []any
	if taskID := strings.TrimSpace(filter.TaskID); taskID != "" {
		conditions = append(conditions, "c_task_id = ?")
		args = append(args, taskID)
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
		SELECT `+recalcJobColumns+`
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
