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
	taskRules    *CollectionTaskRepository
	taskItems    *TaskInstanceRepository
	fetchBatches *FetchBatchRepository
	fetchRetries *FetchRetryRepository
	periods      *PeriodReadinessRepository
}

// DeleteTaskRuntime removes all Collector-owned execution state for one task
// before the task row itself is deleted.
func (s *Store) DeleteTaskRuntime(ctx context.Context, spaceID, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM t_period_readiness_items WHERE c_task_id IN (SELECT c_instance_id FROM t_collector_task_instances WHERE c_space_id = ? AND c_task_id = ?)`, spaceID, taskID).Error; err != nil {
			return err
		}
		for _, query := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM t_collector_task_instances WHERE c_space_id = ? AND c_task_id = ?`, []any{spaceID, taskID}},
			{`DELETE FROM t_collector_fetch_batches WHERE c_space_id = ? AND c_task_id = ?`, []any{spaceID, taskID}},
			{`DELETE FROM t_collector_fetch_retry_items WHERE c_space_id = ? AND c_task_id = ?`, []any{spaceID, taskID}},
		} {
			if err := tx.Exec(query.sql, query.args...).Error; err != nil {
				return err
			}
		}
		// A readiness parent without items can otherwise be interpreted by the
		// finalizer as an already-complete period. Runtime deletion must remove
		// those empty snapshots before the task row is deleted.
		if err := tx.Exec(`DELETE FROM t_period_readiness WHERE c_space_id = ? AND NOT EXISTS (SELECT 1 FROM t_period_readiness_items WHERE c_readiness_id = t_period_readiness.c_id)`, spaceID).Error; err != nil {
			return err
		}
		return nil
	})
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
	s.taskRules = NewCollectionTaskRepository(db)
	s.taskItems = NewTaskInstanceRepository(db)
	s.fetchBatches = NewFetchBatchRepository(db)
	s.fetchRetries = NewFetchRetryRepository(db)
	s.periods = NewPeriodReadinessRepository(db)
	applySQLitePoolConfig(db, opts)
	log.Infof("初始化 Collector SQLite 数据库: %s", dbPath)
	return s, nil
}

// Tasks returns the collection task repository.
func (s *Store) Tasks() *CollectionTaskRepository {
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

// PeriodReadiness returns the durable period completion repository.
func (s *Store) PeriodReadiness() *PeriodReadinessRepository {
	if s == nil {
		return nil
	}
	return s.periods
}

// ApplySchema applies schema SQL during service startup.
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("collector schema sql is empty")
	}
	if err := s.rejectLegacySchema(); err != nil {
		return err
	}
	return s.db.Exec(sql).Error
}

// rejectLegacySchema deliberately fails closed for the pre-task model. There
// is no safe in-place migration for a running Collector because the old rule
// and instance identities have different meanings from the new task/instance
// identities. Operators must use the documented purge/reset procedure before
// starting this binary against an existing database.
func (s *Store) rejectLegacySchema() error {
	legacyTables := []string{"t_collector_task_rules"}
	for _, table := range legacyTables {
		var count int64
		if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count).Error; err != nil {
			return fmt.Errorf("inspect legacy collector table %s: %w", table, err)
		}
		if count > 0 {
			return fmt.Errorf("collector schema reset required: legacy task rule table found (%s)", table)
		}
	}
	for table, columns := range map[string][]string{
		"t_collector_tasks":          {"c_task_name", "c_result_dataset_id", "c_result_view_id"},
		"t_collector_task_instances": {"c_instance_id", "c_task_id", "c_function_name", "c_source_id"},
		"t_period_readiness":         {"c_work_type", "c_committed_positions_json"},
	} {
		var tableCount int64
		if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&tableCount).Error; err != nil {
			return fmt.Errorf("inspect collector table %s: %w", table, err)
		}
		if tableCount == 0 {
			continue
		}
		for _, column := range columns {
			var columnCount int64
			if err := s.db.Raw(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&columnCount).Error; err != nil {
				return fmt.Errorf("inspect collector schema %s.%s: %w", table, column, err)
			}
			if columnCount == 0 {
				return fmt.Errorf("collector schema reset required: current task schema is incomplete (%s.%s missing)", table, column)
			}
		}
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
		// Collector persists assignment and readiness state that drives the
		// market timers.  Synchronous=OFF can leave b-tree pages torn after a
		// process or host crash, which surfaces later as "database disk image is
		// malformed" and stops completion events from advancing readiness.
		// NORMAL keeps WAL throughput while preserving the WAL durability
		// guarantee needed by this control-plane database.
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(ON)",
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
