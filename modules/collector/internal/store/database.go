// Package store owns Collector's SQLite connection and persistence repositories.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Store owns the Collector SQLite connection and repositories.
type Store struct {
	db           *gorm.DB
	taskRules    *TaskRuleRepository
	taskItems    *TaskInstanceRepository
	fetchBatches *FetchBatchRepository
	fetchRetries *FetchRetryRepository
}

// Options configures the Collector SQLite store.
type Options struct {
	Path            string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open opens the Collector SQLite store. Schema creation is handled by bootstrap.
func Open(opts *Options) (*Store, error) {
	dbPath := "./data/moox_collector.db"
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
	s := &Store{db: db}
	s.taskRules = NewTaskRuleRepository(db)
	s.taskItems = NewTaskInstanceRepository(db)
	s.fetchBatches = NewFetchBatchRepository(db)
	s.fetchRetries = NewFetchRetryRepository(db)
	applySQLitePoolConfig(db, opts)
	log.Infof("初始化 Collector SQLite 数据库: %s", dbPath)
	return s, nil
}

// TaskRules returns the task rule repository.
func (s *Store) TaskRules() *TaskRuleRepository {
	if s == nil {
		return nil
	}
	return s.taskRules
}

// TaskInstances returns the task instance repository.
func (s *Store) TaskInstances() *TaskInstanceRepository {
	if s == nil {
		return nil
	}
	return s.taskItems
}

func (s *Store) FetchBatches() *FetchBatchRepository {
	if s == nil {
		return nil
	}
	return s.fetchBatches
}

func (s *Store) FetchRetries() *FetchRetryRepository {
	if s == nil {
		return nil
	}
	return s.fetchRetries
}

// ApplySchema applies schema SQL during service startup.
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("collector schema sql is empty")
	}
	// The schema is also applied to an existing Collector database during a
	// rolling deploy. SQLite executes the schema as one batch, so add the new
	// assignment column before the batch reaches its dependent index.
	if err := s.ensureTaskInstanceFunctionColumn(); err != nil {
		return err
	}
	return s.db.Exec(sql).Error
}

func (s *Store) ensureTaskInstanceFunctionColumn() error {
	var tableCount int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, "t_collector_task_instances").Scan(&tableCount).Error; err != nil {
		return fmt.Errorf("check task instance table: %w", err)
	}
	if tableCount == 0 {
		return nil
	}
	rows, err := s.db.Raw("PRAGMA table_info(t_collector_task_instances)").Rows()
	if err != nil {
		return fmt.Errorf("inspect task instance columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan task instance column: %w", err)
		}
		if name == "c_function_name" {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect task instance columns: %w", err)
	}
	if err := s.db.Exec(`ALTER TABLE t_collector_task_instances ADD COLUMN c_function_name TEXT NOT NULL DEFAULT ''`).Error; err != nil {
		return fmt.Errorf("add task instance function column: %w", err)
	}
	return nil
}

// Ping verifies that the database is available.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
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
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(OFF)",
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
	// Collector has one local SQLite database. Keeping one connection avoids
	// scheduler writes contending with completion-consumer writes; WAL helps
	// readers but still permits only one writer.
	maxOpen := 1
	maxIdle := 1
	if cfg != nil {
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
