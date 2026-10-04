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
	c_space_id, c_job_id, c_job_item_id, c_job_type, c_params, c_priority, c_execute_at,
	c_status, c_result_summary, c_last_error_kind, c_last_error_code, c_last_error_message,
	c_duration_ms, c_execution_node, c_finish_time, c_ctime, c_mtime
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(c_space_id, c_job_item_id) DO UPDATE SET
	c_job_id = excluded.c_job_id,
	c_job_type = excluded.c_job_type,
	c_params = excluded.c_params,
	c_priority = excluded.c_priority,
	c_execute_at = excluded.c_execute_at,
	c_status = excluded.c_status,
	c_result_summary = excluded.c_result_summary,
	c_last_error_kind = excluded.c_last_error_kind,
	c_last_error_code = excluded.c_last_error_code,
	c_last_error_message = excluded.c_last_error_message,
	c_duration_ms = excluded.c_duration_ms,
	c_execution_node = excluded.c_execution_node,
	c_finish_time = excluded.c_finish_time,
	c_mtime = excluded.c_mtime
`, state.SpaceID, state.JobID, state.JobItemID, state.JobType, params, state.Priority, state.ExecuteAt,
		state.Status, result, state.LastErrorKind, state.LastErrorCode, state.LastErrorMessage,
		state.DurationMS, state.ExecutionNode, state.FinishedAt, state.CreatedAt, state.UpdatedAt).Error; err != nil {
		return fmt.Errorf("upsert job history item: %w", err)
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
