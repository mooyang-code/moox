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
	Members         []string  `json:"members"`
	Subjects        []string  `json:"subjects"`
	MembersOmitted  bool      `json:"-"`
	SubjectsOmitted bool      `json:"-"`
	StartTime       int64     `json:"start_time"`
	EndTime         int64     `json:"end_time"`
	Status          string    `json:"status"`
	ProgressTime    int64     `json:"progress_time"`
	Error           string    `json:"error"`
	EngineID        string    `json:"engine_id"`
	LeaseToken      string    `json:"-"`
	LeaseExpiresAt  int64     `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// recalcJobColumns is the column list every recalc job read selects, in the
// order of recalcJobRow.
const recalcJobColumns = `c_job_id, c_request_id, c_set_id, c_members_json, c_subjects_json,
	 c_members_omitted, c_subjects_omitted, c_start_time, c_end_time, c_status, c_progress_time, c_error,
	 c_engine_id, c_lease_token, c_lease_expires_at, c_ctime, c_mtime`

type recalcJobRow struct {
	JobID           string    `gorm:"column:c_job_id"`
	RequestID       string    `gorm:"column:c_request_id"`
	SetID           string    `gorm:"column:c_set_id"`
	MembersJSON     string    `gorm:"column:c_members_json"`
	SubjectsJSON    string    `gorm:"column:c_subjects_json"`
	MembersOmitted  bool      `gorm:"column:c_members_omitted"`
	SubjectsOmitted bool      `gorm:"column:c_subjects_omitted"`
	StartTime       int64     `gorm:"column:c_start_time"`
	EndTime         int64     `gorm:"column:c_end_time"`
	Status          string    `gorm:"column:c_status"`
	ProgressTime    int64     `gorm:"column:c_progress_time"`
	Error           string    `gorm:"column:c_error"`
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
	return RecalcJob{
		JobID: r.JobID, RequestID: r.RequestID, SetID: r.SetID,
		Members: members, Subjects: subjects,
		MembersOmitted: r.MembersOmitted, SubjectsOmitted: r.SubjectsOmitted, StartTime: r.StartTime,
		EndTime: r.EndTime, Status: r.Status, ProgressTime: r.ProgressTime,
		Error: r.Error, EngineID: r.EngineID, LeaseToken: r.LeaseToken, LeaseExpiresAt: r.LeaseExpiresAt,
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

// EnableMemberWithRecalcJob atomically accepts a member's initial backfill and
// makes the member visible to live trigger selection.
func (s *Store) EnableMemberWithRecalcJob(ctx context.Context, setID, alias, expectedStatus string, job RecalcJob) (RecalcJob, error) {
	if s == nil || s.db == nil {
		return RecalcJob{}, fmt.Errorf("factor database is not open")
	}
	setID = strings.TrimSpace(setID)
	alias = strings.TrimSpace(alias)
	if setID == "" || alias == "" || setID != strings.TrimSpace(job.SetID) ||
		expectedStatus != domain.MemberStatusDisabled || job.Status != RecalcStatusAccepted {
		return RecalcJob{}, fmt.Errorf("factor activation request is invalid")
	}
	job, membersJSON, subjectsJSON, err := normalizeRecalcJob(job)
	if err != nil {
		return RecalcJob{}, err
	}
	if !containsString(job.Members, alias) {
		return RecalcJob{}, fmt.Errorf("factor activation job does not include member %q", alias)
	}
	var stored RecalcJob
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 同一个因子集连续启用多个成员时，只保留一个还没开始跑的回填任务，把新成员并进去：
		// 每个成员各建一个全量回填任务，会让同一个窗口被重复读 N 遍（11 个因子就是 11 遍）。
		merged, mergeErr := mergeQueuedEnableBackfill(tx, job, alias)
		if mergeErr != nil {
			return mergeErr
		}
		var created bool
		var insertErr error
		if merged != nil {
			stored, created = *merged, true
		} else {
			stored, created, insertErr = insertRecalcJob(tx, job, membersJSON, subjectsJSON)
			if insertErr != nil {
				return insertErr
			}
			if !created && !sameRecalcRequest(stored, job) {
				return fmt.Errorf("%w: request_id %q belongs to a different recalc request", ErrConflict, job.RequestID)
			}
		}
		result := tx.Exec(`
			UPDATE t_factor_set_members SET c_status = ?, c_mtime = ?
			WHERE c_set_id = ? AND c_member_alias = ? AND c_status = ?
		`, domain.MemberStatusEnabled, time.Now().UTC(), setID, alias, expectedStatus)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected > 0 {
			return nil
		}
		var current struct {
			Status string `gorm:"column:c_status"`
		}
		lookup := tx.Raw(`SELECT c_status FROM t_factor_set_members WHERE c_set_id = ? AND c_member_alias = ?`, setID, alias).Scan(&current)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		if !created && current.Status == domain.MemberStatusEnabled {
			return nil
		}
		return fmt.Errorf("%w: member %q of set %q status is %q, expected %q", ErrConflict, alias, setID, current.Status, expectedStatus)
	})
	if err != nil {
		return RecalcJob{}, err
	}
	return stored, nil
}

// enableBackfillRequestPrefix 是启用成员时生成的回填任务的请求号前缀，只有这类任务才会被合并。
const enableBackfillRequestPrefix = "factor-enable-"

// mergeQueuedEnableBackfill 把新启用的成员并进同一因子集里排队中、尚未被引擎领取的回填任务，窗口取两者的并集；
// 没有可合并的任务时返回 nil，由调用方新建。手工补算（请求号不是 factor-enable- 开头）、已领取或已结束的任务不合并。
func mergeQueuedEnableBackfill(tx *gorm.DB, job RecalcJob, alias string) (*RecalcJob, error) {
	var row recalcJobRow
	lookup := tx.Raw(`
		SELECT `+recalcJobColumns+`
		FROM t_factor_recalc_jobs
		WHERE c_set_id = ? AND c_status = ? AND c_engine_id = '' AND c_lease_token = '' AND c_request_id LIKE ?
		  AND c_subjects_omitted = 0 AND c_members_omitted = 0
		ORDER BY c_ctime, c_job_id LIMIT 1
	`, job.SetID, RecalcStatusAccepted, enableBackfillRequestPrefix+"%").Scan(&row)
	if lookup.Error != nil {
		return nil, lookup.Error
	}
	if lookup.RowsAffected == 0 {
		return nil, nil
	}
	existing, err := row.job()
	if err != nil {
		return nil, err
	}
	if len(existing.Subjects) != len(job.Subjects) {
		return nil, nil
	}
	members := append([]string(nil), existing.Members...)
	if !containsString(members, alias) {
		members = append(members, alias)
	}
	sort.Strings(members)
	membersJSON, err := marshalStringSlice(members)
	if err != nil {
		return nil, err
	}
	start, end := min(existing.StartTime, job.StartTime), max(existing.EndTime, job.EndTime)
	result := tx.Exec(`
		UPDATE t_factor_recalc_jobs SET c_members_json = ?, c_start_time = ?, c_end_time = ?, c_mtime = ?
		WHERE c_job_id = ? AND c_status = ? AND c_engine_id = '' AND c_lease_token = ''
	`, membersJSON, start, end, time.Now().UTC(), existing.JobID, RecalcStatusAccepted)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	existing.Members, existing.StartTime, existing.EndTime = members, start, end
	return &existing, nil
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
		(c_job_id, c_request_id, c_set_id, c_members_json, c_subjects_json,
		 c_members_omitted, c_subjects_omitted,
		 c_start_time, c_end_time, c_status, c_progress_time, c_error, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_request_id) DO NOTHING
	`, job.JobID, job.RequestID, job.SetID, membersJSON, subjectsJSON,
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
	if left.RequestID != right.RequestID || left.SetID != right.SetID ||
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
	SetID    string
	Statuses []string
}

// ListRecalcJobs returns jobs newest first, optionally restricted to one factor set and statuses.
func (s *Store) ListRecalcJobs(ctx context.Context, filter RecalcJobFilter) ([]RecalcJob, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	query := `SELECT ` + recalcJobColumns + `
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
