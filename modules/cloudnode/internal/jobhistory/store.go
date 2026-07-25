package jobhistory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const defaultDir = "../data/cloudnode/jobs"

type StoreOptions struct {
	Dir           string
	RetentionDays int
}

type Store struct {
	dir           string
	retentionDays int
}

func NewStore(opts StoreOptions) *Store {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		dir = defaultDir
	}
	retentionDays := opts.RetentionDays
	if retentionDays <= 0 {
		retentionDays = 2
	}
	return &Store{dir: dir, retentionDays: retentionDays}
}

func (s *Store) WriteTerminal(ctx context.Context, state jobstate.State) error {
	if !state.IsTerminal() {
		return nil
	}
	day := state.UpdatedAt
	if state.FinishedAt != nil && !state.FinishedAt.IsZero() {
		day = *state.FinishedAt
	}
	if day.IsZero() {
		day = time.Now()
	}
	db, err := s.openDayDB(ctx, day)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	params := jsonString(state.Params)
	result := jsonString(state.ResultSummary)
	if err := db.WithContext(ctx).Exec(`
INSERT INTO t_cloud_job_items (
	c_space_id, c_job_id, c_job_item_id, c_job_type, c_code_package_id, c_params, c_priority,
	c_status, c_running_node, c_attempt_no, c_result_summary, c_last_error_kind,
	c_last_error_code, c_last_error_message, c_start_time, c_finish_time, c_ctime, c_mtime
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(c_space_id, c_job_item_id) DO UPDATE SET
	c_job_id = excluded.c_job_id,
	c_job_type = excluded.c_job_type,
	c_code_package_id = excluded.c_code_package_id,
	c_params = excluded.c_params,
	c_priority = excluded.c_priority,
	c_status = excluded.c_status,
	c_running_node = excluded.c_running_node,
	c_attempt_no = excluded.c_attempt_no,
	c_result_summary = excluded.c_result_summary,
	c_last_error_kind = excluded.c_last_error_kind,
	c_last_error_code = excluded.c_last_error_code,
	c_last_error_message = excluded.c_last_error_message,
	c_start_time = excluded.c_start_time,
	c_finish_time = excluded.c_finish_time,
	c_mtime = excluded.c_mtime
`, state.SpaceID, state.JobID, state.JobItemID, state.JobType, state.CodePackageID, params, state.Priority,
		state.Status, state.RunningNode, state.AttemptNo, result, state.LastErrorKind,
		state.LastErrorCode, state.LastErrorMessage, state.StartedAt, state.FinishedAt, state.CreatedAt, state.UpdatedAt).Error; err != nil {
		return fmt.Errorf("upsert job history item: %w", err)
	}
	for _, attempt := range state.Attempts {
		if err := db.WithContext(ctx).Exec(`
INSERT INTO t_cloud_job_item_attempts (
	c_space_id, c_job_item_id, c_attempt_no, c_node_id, c_status, c_error_kind,
	c_error_code, c_error_message, c_result_summary, c_started_at, c_finished_at, c_ctime, c_mtime
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(c_space_id, c_job_item_id, c_attempt_no) DO UPDATE SET
	c_node_id = excluded.c_node_id,
	c_status = excluded.c_status,
	c_error_kind = excluded.c_error_kind,
	c_error_code = excluded.c_error_code,
	c_error_message = excluded.c_error_message,
	c_result_summary = excluded.c_result_summary,
	c_started_at = excluded.c_started_at,
	c_finished_at = excluded.c_finished_at,
	c_mtime = excluded.c_mtime
`, state.SpaceID, state.JobItemID, attempt.AttemptNo, attempt.NodeID, attempt.Status, attempt.ErrorKind,
			attempt.ErrorCode, attempt.ErrorMessage, jsonString(attempt.ResultSummary), attempt.StartedAt, attempt.FinishedAt, state.CreatedAt, state.UpdatedAt).Error; err != nil {
			return fmt.Errorf("upsert job history attempt: %w", err)
		}
	}
	return nil
}

func (s *Store) openDayDB(ctx context.Context, day time.Time) (*gorm.DB, error) {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("create job history dir %s: %w", s.dir, err)
	}
	db, err := gorm.Open(sqlite.Open(s.dayPath(day)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open job history day db: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get job history sql db: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := db.WithContext(ctx).Exec(SchemaSQL).Error; err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("init job history schema: %w", err)
	}
	return db, nil
}

func (s *Store) dayPath(day time.Time) string {
	return filepath.Join(s.dir, calendarDay(day).Format("20060102")+".db")
}

func jsonString(values map[string]any) string {
	if values == nil {
		return "{}"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
