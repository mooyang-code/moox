// Package store persists Factor configuration and asynchronous recalc jobs.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"gorm.io/gorm"
)

var ErrConflict = errors.New("factor store conflict")

// Store owns the Factor SQLite connection.
type Store struct {
	db *gorm.DB
}

// SQLDB 返回底层数据库连接（重置时备份与清表用，由生命周期的重置实现调用）。
func (s *Store) SQLDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("factor store is not open")
	}
	return s.db.DB()
}

// Options configures the Factor SQLite store.
type Options struct {
	Path            string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open opens the SQLite store and creates its schema on a fresh database.
func Open(opts *Options) (*Store, error) {
	dbPath := "./data/factor/factor.db"
	if opts != nil && opts.Path != "" {
		dbPath = opts.Path
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	applySQLitePoolConfig(db, opts)
	s := &Store{db: db}
	if err := s.ApplySchema(factorschema.AllSQL()); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("initialize factor schema: %w", err)
	}
	return s, nil
}

// ApplySchema executes the embedded schema on a new database and rejects a
// database whose Factor tables do not match the current schema.
// 已有库缺少后来新增的表时由 CREATE TABLE IF NOT EXISTS 补齐；已有的表必须与当前结构一致。
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("factor schema sql is empty")
	}
	existing, err := s.factorSchemaTables()
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		if err := s.validateSchemaTables(existing, false); err != nil {
			return err
		}
	}
	if err := s.db.Exec(sql).Error; err != nil {
		return err
	}
	tables, err := s.factorSchemaTables()
	if err != nil {
		return err
	}
	return s.validateSchemaTables(tables, true)
}

func (s *Store) factorSchemaTables() ([]string, error) {
	var tables []string
	if err := s.db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't_factor_%' ORDER BY name",
	).Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("inspect factor schema tables: %w", err)
	}
	return tables, nil
}

// validateSchemaTables 检查表结构；requireAll 为 false 时允许缺少表（建表前检查已有库）。
func (s *Store) validateSchemaTables(tables []string, requireAll bool) error {
	expected := map[string][]string{
		"t_factor_categories": {
			"c_category_id", "c_name", "c_sort_order", "c_ctime", "c_mtime",
		},
		"t_factor_defs": {
			"c_factor_id", "c_name", "c_category_id", "c_spec", "c_spec_hash", "c_allow_partial_universe", "c_ctime", "c_mtime",
		},
		"t_factor_task_members": {
			"c_task_id", "c_member_key", "c_factor_id", "c_kind", "c_status", "c_params", "c_last_submitted_params",
			"c_last_submitted_period", "c_last_request_id", "c_last_submitted_at", "c_ctime", "c_mtime",
		},
		"t_factor_recalc_jobs": {
			"c_job_id", "c_request_id", "c_task_id", "c_members_json", "c_subjects_json",
			"c_members_omitted", "c_subjects_omitted",
			"c_start_time", "c_end_time", "c_status", "c_progress_time", "c_error",
			"c_default_filled_json", "c_engine_id", "c_lease_token", "c_lease_expires_at", "c_ctime", "c_mtime",
		},
		"t_factor_tasks": {
			"c_task_id", "c_space_id", "c_name", "c_source_dataset_id", "c_freq",
			"c_result_dataset_id", "c_bound_instance_id", "c_status", "c_ctime", "c_mtime",
		},
		"t_factor_engine_heartbeat": {
			"c_id", "c_engine_id", "c_boot_id", "c_heartbeat_at", "c_mtime",
		},
		"t_factor_udfs": {
			"c_name", "c_active_hash", "c_ctime", "c_mtime",
		},
		"t_factor_udf_versions": {
			"c_hash", "c_name", "c_manifest", "c_wasm", "c_source_ref", "c_sdk_version", "c_toolchain", "c_report", "c_ctime",
		},
		"t_factor_recalc_job_bindings": {
			"c_job_id", "c_bindings", "c_ctime",
		},
	}
	if requireAll && len(tables) != len(expected) {
		return fmt.Errorf("factor database must contain only the current factor tables; create a fresh database")
	}
	for _, table := range tables {
		want, ok := expected[table]
		if !ok {
			return fmt.Errorf("factor database contains unexpected table %s; create a fresh database", table)
		}
		var columns []string
		if err := s.db.Raw("SELECT name FROM pragma_table_info(?) ORDER BY cid", table).Scan(&columns).Error; err != nil {
			return fmt.Errorf("inspect factor schema table %s: %w", table, err)
		}
		if strings.Join(columns, "\x00") != strings.Join(want, "\x00") {
			return fmt.Errorf("factor database table %s uses an obsolete schema; create a fresh database", table)
		}
	}
	for name := range expected {
		if requireAll && !contains(tables, name) {
			return fmt.Errorf("factor database missing table %s; create a fresh database", name)
		}
	}
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// WithTx runs fn inside a Factor SQLite transaction.
func (s *Store) WithTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if fn == nil {
		return fmt.Errorf("factor transaction callback is required")
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

// Ping verifies that the database is available.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// Close releases the underlying SQL connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func buildSQLiteDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=foreign_keys(ON)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-64000)",
		"_pragma=wal_autocheckpoint(1000)",
	}
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + strings.Join(pragmas, "&")
}

func applySQLitePoolConfig(db *gorm.DB, cfg *Options) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	maxOpen := 1
	maxIdle := 1
	if cfg != nil {
		if cfg.MaxOpenConns > 0 {
			maxOpen = cfg.MaxOpenConns
		}
		if cfg.MaxIdleConns > 0 && cfg.MaxIdleConns < maxOpen {
			maxIdle = cfg.MaxIdleConns
		}
		if cfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime > 0 {
			sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
}
