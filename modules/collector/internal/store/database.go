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
	db                   *gorm.DB
	taskRepo             *TaskRepository
	taskItems            *TaskInstanceRepository
	fetchBatches         *FetchBatchRepository
	runs                 *RunRepository
	fetchRetries         *FetchRetryRepository
	periods              *PeriodReadinessRepository
	periodSeriesSnapshot *PeriodSeriesSnapshotRepository
	periodStorageStates  *PeriodStorageStateRepository
}

// DeleteTaskRuntime removes all Collector-owned execution state for one task
// before the task row itself is deleted.
func (s *Store) DeleteTaskRuntime(ctx context.Context, spaceID, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Target-level retry/readiness state belongs to the target task. Remove
		// it before deleting the WriteTarget relation itself.
		if err := tx.Exec(`DELETE FROM t_collector_fetch_retry_items WHERE c_space_id = ? AND c_write_target_id IN (SELECT c_write_target_id FROM t_collector_instance_write_targets WHERE c_space_id = ? AND c_task_id = ?) AND NOT (c_status = 'permanent_failed' AND c_period_failure_report_state = 'pending')`, spaceID, spaceID, taskID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM t_period_readiness_items WHERE c_write_target_id IN (SELECT c_write_target_id FROM t_collector_instance_write_targets WHERE c_space_id = ? AND c_task_id = ?)`, spaceID, taskID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM t_collector_instance_write_targets WHERE c_space_id = ? AND c_task_id = ?`, spaceID, taskID).Error; err != nil {
			return err
		}

		// Resample instances are durable local work records, but their ownership
		// also lives in WriteTarget. Once this task's target is removed, delete
		// only resample instances that have become true orphans. Shared market
		// fetch execution history is retained.
		if err := tx.Exec(`DELETE FROM t_collector_task_instances WHERE c_space_id = ? AND c_data_type = 'kline_resample' AND NOT EXISTS (SELECT 1 FROM t_collector_instance_write_targets targets WHERE targets.c_space_id = t_collector_task_instances.c_space_id AND targets.c_instance_id = t_collector_task_instances.c_instance_id)`, spaceID).Error; err != nil {
			return err
		}

		// A readiness parent without items can otherwise be interpreted by the
		// finalizer as an already-complete period.
		if err := tx.Exec(`DELETE FROM t_period_readiness WHERE c_space_id = ? AND NOT EXISTS (SELECT 1 FROM t_period_readiness_items WHERE c_readiness_id = t_period_readiness.c_id)`, spaceID).Error; err != nil {
			return err
		}
		return nil
	})
}

// HasOtherDatasetWriteTargets reports whether another CollectionTask still
// references a result Dataset. Task-owned result Datasets are normally
// isolated, but this guard prevents destructive metadata deletion if corrupted
// or hand-written runtime state introduced a shared reference.
func (s *Store) HasOtherDatasetWriteTargets(ctx context.Context, spaceID, datasetID, taskID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("collector database is not open")
	}
	spaceID, datasetID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID), strings.TrimSpace(taskID)
	if spaceID == "" || datasetID == "" || taskID == "" {
		return false, fmt.Errorf("space_id, dataset_id and task_id are required")
	}
	var count int64
	if err := s.db.WithContext(ctx).Table("t_collector_instance_write_targets").
		Where("c_space_id = ? AND c_dataset_id = ? AND c_task_id <> ?", spaceID, datasetID, taskID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// WaitTaskDrain waits until no planned or dispatched batch still references a
// task. Disabling a task prevents new planning, but an already invoked SCF may
// still write to Storage; destructive result deletion must fail closed until
// that batch reaches a terminal state.
func (s *Store) WaitTaskDrain(ctx context.Context, spaceID, taskID string, maxWait time.Duration) error {
	if s == nil || s.fetchBatches == nil {
		return fmt.Errorf("collector batch repository is not initialized")
	}
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	deadline := time.Now().Add(maxWait)
	for {
		active, err := s.fetchBatches.HasActiveTask(ctx, strings.TrimSpace(spaceID), strings.TrimSpace(taskID))
		if err != nil {
			return fmt.Errorf("check task drain state: %w", err)
		}
		if !active {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("task %s/%s still has an active fetch batch", strings.TrimSpace(spaceID), strings.TrimSpace(taskID))
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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
	s.taskRepo = NewTaskRepository(db)
	s.taskItems = NewTaskInstanceRepository(db)
	s.fetchBatches = NewFetchBatchRepository(db)
	s.runs = NewRunRepository(db)
	s.fetchRetries = NewFetchRetryRepository(db)
	s.periods = NewPeriodReadinessRepository(db)
	s.periodSeriesSnapshot = NewPeriodSeriesSnapshotRepository(db)
	s.periodStorageStates = NewPeriodStorageStateRepository(db)
	applySQLitePoolConfig(db, opts)
	log.Infof("初始化 Collector SQLite 数据库: %s", dbPath)
	return s, nil
}

// Tasks returns the collection task repository.
func (s *Store) Tasks() *TaskRepository {
	if s == nil {
		return nil
	}
	return s.taskRepo
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

func (s *Store) Runs() *RunRepository { return s.runs }

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

// PeriodSeriesSnapshot returns the immutable Dataset period series snapshot repository.
func (s *Store) PeriodSeriesSnapshot() *PeriodSeriesSnapshotRepository {
	if s == nil {
		return nil
	}
	return s.periodSeriesSnapshot
}

// PeriodStorageStates returns the authoritative Storage observation repository.
func (s *Store) PeriodStorageStates() *PeriodStorageStateRepository {
	if s == nil {
		return nil
	}
	return s.periodStorageStates
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
	if err := s.db.Exec(sql).Error; err != nil {
		return err
	}
	return s.validatePeriodFailureSchema()
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
		"t_collector_tasks": {
			"c_id", "c_space_id", "c_task_id", "c_task_name", "c_description",
			"c_data_type", "c_definition_hash", "c_series_hash", "c_collect_params",
			"c_enabled", "c_creator", "c_prepare_state", "c_last_error",
			"c_result_dataset_id", "c_result_view_id", "c_coverage_start_time",
			"c_ctime", "c_mtime",
		},
		"t_collector_task_tags": {
			"c_space_id", "c_task_id", "c_tag_id", "c_ctime",
		},
		"t_collector_task_series": {
			"c_id", "c_space_id", "c_task_id", "c_series_index", "c_series_key", "c_subject_id",
			"c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag", "c_ctime", "c_mtime",
		},
		"t_collector_task_period_series": {
			"c_id", "c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_series_index", "c_series_key",
			"c_subject_id", "c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag",
			"c_series_hash", "c_expected_count", "c_ctime",
		},
		"t_collector_period_storage_states": {
			"c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_series_hash",
			"c_expected_count", "c_deadline_at", "c_status", "c_confirmed_at",
		},
		"t_collector_runs": {
			"c_id", "c_space_id", "c_run_id", "c_run_key", "c_run_type", "c_frequency",
			"c_status", "c_target_time", "c_error_summary", "c_ctime", "c_mtime",
		},
		"t_collector_task_instances": {
			"c_id", "c_space_id", "c_instance_id", "c_run_id", "c_request_key", "c_provider", "c_provider_symbol",
			"c_market_type", "c_data_type", "c_subject_id", "c_frequency", "c_target_data_time", "c_source_id", "c_series_tag",
			"c_function_name", "c_last_exec_status", "c_task_params", "c_last_exec_time",
			"c_result", "c_is_deleted", "c_ctime", "c_mtime",
		},
		"t_collector_instance_write_targets": {
			"c_id", "c_space_id", "c_write_target_id", "c_instance_id", "c_task_id",
			"c_dataset_id", "c_view_id", "c_output_fields_json", "c_status", "c_attempt",
			"c_next_retry_at", "c_last_error", "c_ctime", "c_mtime",
		},
		"t_collector_fetch_batches": {
			"c_id", "c_space_id", "c_batch_id", "c_parent_batch_id", "c_schedule_id",
			"c_batch_kind", "c_shard_index", "c_instance_id", "c_write_target_id", "c_retry_scope", "c_frequency",
			"c_region", "c_node_id", "c_function_name", "c_status", "c_attempt",
			"c_request_id", "c_request_json", "c_planned_count", "c_success_count",
			"c_retry_count", "c_permanent_failed_count", "c_error_summary",
			"c_late_completion", "c_planned_at", "c_dispatched_at", "c_deadline_at",
			"c_completed_at", "c_ctime", "c_mtime",
		},
		"t_collector_fetch_batch_items": {
			"c_space_id", "c_batch_id", "c_instance_id", "c_status", "c_error", "c_completed_at",
		},
		"t_collector_fetch_retry_items": {
			"c_id", "c_space_id", "c_retry_key", "c_source_batch_id", "c_batch_kind",
			"c_instance_id", "c_write_target_id", "c_retry_scope", "c_subject_id", "c_frequency",
			"c_target_data_time", "c_task_json", "c_failure_targets_json", "c_attempt", "c_status",
			"c_period_failure_report_state", "c_period_failure_results_json", "c_period_failure_last_error", "c_period_failure_deadline_exceeded_at",
			"c_next_retry_at", "c_last_error_type", "c_last_error_summary",
			"c_ctime", "c_mtime",
		},
		"t_period_readiness": {
			"c_id", "c_space_id", "c_dataset_id", "c_frequency", "c_work_type",
			"c_period_time", "c_deadline_at", "c_status", "c_report_state",
			"c_event_id", "c_collected_at", "c_payload_json",
			"c_committed_positions_json", "c_ctime", "c_mtime",
		},
		"t_period_readiness_items": {
			"c_readiness_id", "c_instance_id", "c_write_target_id", "c_subject_id", "c_series_tag", "c_function_name",
			"c_write_source", "c_required_fields_json", "c_state", "c_updated_at",
		},
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
	var instanceTableCount int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 't_collector_task_instances'`).Scan(&instanceTableCount).Error; err != nil {
		return fmt.Errorf("inspect collector task instance table: %w", err)
	}
	if instanceTableCount > 0 {
		for _, column := range []string{"c_task_id", "c_dataset_id"} {
			var columnCount int64
			if err := s.db.Raw(`SELECT count(*) FROM pragma_table_info('t_collector_task_instances') WHERE name = ?`, column).Scan(&columnCount).Error; err != nil {
				return fmt.Errorf("inspect collector task instance legacy owner column %s: %w", column, err)
			}
			if columnCount > 0 {
				return fmt.Errorf("collector schema reset required: task instance legacy owner column found (t_collector_task_instances.%s)", column)
			}
		}
	}
	var batchTableCount int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 't_collector_fetch_batches'`).Scan(&batchTableCount).Error; err != nil {
		return fmt.Errorf("inspect collector fetch batch table: %w", err)
	}
	if batchTableCount > 0 {
		for _, column := range []string{"c_task_id", "c_dataset_id"} {
			var columnCount int64
			if err := s.db.Raw(`SELECT count(*) FROM pragma_table_info('t_collector_fetch_batches') WHERE name = ?`, column).Scan(&columnCount).Error; err != nil {
				return fmt.Errorf("inspect collector fetch batch legacy owner column %s: %w", column, err)
			}
			if columnCount > 0 {
				return fmt.Errorf("collector schema reset required: fetch batch legacy owner column found (t_collector_fetch_batches.%s)", column)
			}
		}
	}
	var retryTableCount int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 't_collector_fetch_retry_items'`).Scan(&retryTableCount).Error; err != nil {
		return fmt.Errorf("inspect collector fetch retry table: %w", err)
	}
	if retryTableCount > 0 {
		var legacyReceiptCount int64
		if err := s.db.Raw(`SELECT count(*) FROM pragma_table_info('t_collector_fetch_retry_items') WHERE name = 'c_period_failure_reported'`).Scan(&legacyReceiptCount).Error; err != nil {
			return fmt.Errorf("inspect obsolete period failure acknowledgement column: %w", err)
		}
		if legacyReceiptCount > 0 {
			return fmt.Errorf("collector schema reset required: obsolete period failure boolean found (t_collector_fetch_retry_items.c_period_failure_reported)")
		}
		for _, column := range []string{"c_task_id", "c_dataset_id"} {
			var columnCount int64
			if err := s.db.Raw(`SELECT count(*) FROM pragma_table_info('t_collector_fetch_retry_items') WHERE name = ?`, column).Scan(&columnCount).Error; err != nil {
				return fmt.Errorf("inspect collector fetch retry legacy owner column %s: %w", column, err)
			}
			if columnCount > 0 {
				return fmt.Errorf("collector schema reset required: fetch retry legacy owner column found (t_collector_fetch_retry_items.%s)", column)
			}
		}
	}
	return nil
}

func (s *Store) validatePeriodFailureSchema() error {
	var tableSQL string
	if err := s.db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 't_collector_fetch_retry_items'`).Scan(&tableSQL).Error; err != nil {
		return fmt.Errorf("inspect period failure schema constraint: %w", err)
	}
	if !strings.Contains(strings.ToLower(tableSQL), "check (c_period_failure_report_state in ('pending', 'acknowledged', 'missed_deadline'))") {
		return fmt.Errorf("collector schema reset required: period failure report state CHECK constraint is missing")
	}
	var invalidStates int64
	if err := s.db.Raw(`SELECT count(*) FROM t_collector_fetch_retry_items WHERE c_period_failure_report_state NOT IN ('pending', 'acknowledged', 'missed_deadline')`).Scan(&invalidStates).Error; err != nil {
		return fmt.Errorf("validate period failure report states: %w", err)
	}
	if invalidStates > 0 {
		return fmt.Errorf("collector schema contains %d invalid period failure report states", invalidStates)
	}
	var indexCount int64
	if err := s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_collector_fetch_retry_period_failure'`).Scan(&indexCount).Error; err != nil {
		return fmt.Errorf("inspect period failure report index: %w", err)
	}
	if indexCount != 1 {
		return fmt.Errorf("collector schema reset required: period failure report index is missing")
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
