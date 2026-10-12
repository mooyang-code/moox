// Package store owns Collector's SQLite connection and persistence repositories.
package store

import (
	"context"
	"fmt"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
	"trpc.group/trpc-go/trpc-go/log"
)

// Store owns the Collector SQLite connection and repositories.
type Store struct {
	db         *gorm.DB
	path       string
	taskRepo   *TaskRepository
	taskItems  *TaskInstanceRepository
	periods    *PeriodReadinessRepository
	fetchPlans *FetchPlanRepository
	backfill   *BackfillRepository
}

// DeleteTaskRuntime removes all Collector-owned execution state for one task
// before the task row itself is deleted.
func (s *Store) DeleteTaskRuntime(ctx context.Context, spaceID, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 目标级的就绪状态属于目标任务，先于 WriteTarget 关系本身删除。
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
	// 查不到记录是调用方用 ErrRecordNotFound 判断的正常结果，不能每次都当成错误打日志。
	quiet := gormlogger.New(stdlog.New(os.Stderr, "\r\n", stdlog.LstdFlags), gormlogger.Config{SlowThreshold: 200 * time.Millisecond, LogLevel: gormlogger.Warn, IgnoreRecordNotFoundError: true})
	db, err := gorm.Open(sqlite.Dialector{DriverName: "sqlite", DSN: buildSQLiteDSN(dbPath)}, &gorm.Config{Logger: quiet})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, path: dbPath}
	s.taskRepo = NewTaskRepository(db)
	s.taskItems = NewTaskInstanceRepository(db)
	s.periods = NewPeriodReadinessRepository(db)
	s.fetchPlans = NewFetchPlanRepository(db)
	s.backfill = NewBackfillRepository(db)
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

// PeriodReadiness returns the durable period completion repository.
func (s *Store) PeriodReadiness() *PeriodReadinessRepository {
	if s == nil {
		return nil
	}
	return s.periods
}

// FetchPlans returns the SCF fetch plan repository.
func (s *Store) FetchPlans() *FetchPlanRepository {
	if s == nil {
		return nil
	}
	return s.fetchPlans
}

// Backfill returns the K-line backfill job repository.
func (s *Store) Backfill() *BackfillRepository {
	if s == nil {
		return nil
	}
	return s.backfill
}

// ApplySchema applies schema SQL during service startup.
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("collector schema sql is empty")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec(sql).Error
	})
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
		// Keep timestamps readable by SQLite date functions and consistent in
		// equality/range queries; the driver's default includes a zone name.
		"_time_format=sqlite",
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
