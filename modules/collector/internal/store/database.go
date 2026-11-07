// Package store owns Collector's SQLite connection and persistence repositories.
package store

import (
	"context"
	"database/sql"
	"errors"
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
// SQLDB 返回底层数据库连接（重置时备份与清表用，由生命周期的重置实现调用）。
func (s *Store) SQLDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("collector store is not open")
	}
	return s.db.DB()
}

type Store struct {
	db         *gorm.DB
	path       string
	taskRepo   *TaskRepository
	fetchPlans *FetchPlanRepository
	ingest     *IngestModeRepository
	workers    *StreamWorkerRepository
	exclusions *SubjectExclusionRepository
	tagRuns    *TagSyncRunRepository
	backfill   *BackfillRepository
	resample   *ResampleStateRepository
	delist     *DelistNoticeRepository
}

// DeleteTaskRuntime removes all Collector-owned execution state for one task
// before the task row itself is deleted.
func (s *Store) DeleteTaskRuntime(ctx context.Context, spaceID, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 合成任务的目标快照与历史作业属于任务本身。
		if err := tx.Exec(`DELETE FROM t_collector_resample_snapshots WHERE c_space_id = ? AND c_task_id = ?`, spaceID, taskID).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM t_collector_resample_jobs WHERE c_space_id = ? AND c_task_id = ?`, spaceID, taskID).Error
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
	if err := s.db.WithContext(ctx).Table("t_collector_tasks").
		Where("c_space_id = ? AND c_result_dataset_id = ? AND c_task_id <> ?", spaceID, datasetID, taskID).
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
	s.fetchPlans = NewFetchPlanRepository(db)
	s.ingest = NewIngestModeRepository(db)
	s.workers = NewStreamWorkerRepository(db)
	s.exclusions = NewSubjectExclusionRepository(db)
	s.tagRuns = NewTagSyncRunRepository(db)
	s.backfill = NewBackfillRepository(db)
	s.resample = NewResampleStateRepository(db)
	s.delist = NewDelistNoticeRepository(db)
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

// FetchPlans returns the SCF fetch plan repository.
func (s *Store) FetchPlans() *FetchPlanRepository {
	if s == nil {
		return nil
	}
	return s.fetchPlans
}

// IngestModes returns the per-dataset ingest mode repository.
func (s *Store) IngestModes() *IngestModeRepository {
	if s == nil {
		return nil
	}
	return s.ingest
}

// StreamWorkers returns the collector-stream heartbeat repository.
func (s *Store) StreamWorkers() *StreamWorkerRepository {
	if s == nil {
		return nil
	}
	return s.workers
}

// SubjectExclusions returns the collection subject exclusion repository.
func (s *Store) SubjectExclusions() *SubjectExclusionRepository {
	if s == nil {
		return nil
	}
	return s.exclusions
}

// TagSyncRuns returns the repository of per-tag subject sync run results.
func (s *Store) TagSyncRuns() *TagSyncRunRepository {
	if s == nil {
		return nil
	}
	return s.tagRuns
}

// Resample returns the repository of resample snapshots, history jobs, backfill notices and the local event outbox.
func (s *Store) Resample() *ResampleStateRepository {
	if s == nil {
		return nil
	}
	return s.resample
}

// DelistNotices returns the repository of discovered instrument delist notices.
func (s *Store) DelistNotices() *DelistNoticeRepository {
	if s == nil {
		return nil
	}
	return s.delist
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
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Exec(sql).Error
	}); err != nil {
		return err
	}
	return s.verifySchemaUpgrade()
}

// ErrSchemaNotUpgraded 表示已有的 Collector 库还停留在统一数据集之前的表结构。
var ErrSchemaNotUpgraded = errors.New("collector 数据库表结构过旧")

// verifySchemaUpgrade 检查新增的列与重建的表。ApplySchema 只执行 CREATE TABLE IF NOT EXISTS，旧库不会被补列，
// 不检查的话服务照常启动，补采、合成、已确认缺失的读写却会逐个失败，只在日志里报错。
func (s *Store) verifySchemaUpgrade() error {
	var problems []string
	for _, column := range []struct{ table, name string }{
		{"t_collector_backfill_notices", "c_series_tag"},
		{"t_collector_resample_snapshots", "c_members"},
		{"t_collector_resample_jobs", "c_series_tags"},
		{"t_collector_kline_backfill_chunks", "c_series_tag"},
		{"t_collector_source_absences", "c_series_tag"},
	} {
		var count int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, column.table, column.name).Scan(&count).Error; err != nil {
			return fmt.Errorf("检查表结构 %s: %w", column.table, err)
		}
		if count == 0 {
			problems = append(problems, fmt.Sprintf("%s 缺少列 %s", column.table, column.name))
		}
	}
	if len(problems) == 0 {
		// 两张表的主键、唯一键也要带 c_series_tag（只加列不够，要按迁移脚本重建）。
		var absencePK int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_table_info('t_collector_source_absences') WHERE name = 'c_series_tag' AND pk > 0`).Scan(&absencePK).Error; err != nil {
			return fmt.Errorf("检查表结构 t_collector_source_absences: %w", err)
		}
		if absencePK == 0 {
			problems = append(problems, "t_collector_source_absences 的主键不含 c_series_tag")
		}
		var chunkUnique int64
		if err := s.db.Raw(`SELECT COUNT(*) FROM pragma_index_list('t_collector_kline_backfill_chunks') AS idx
			JOIN pragma_index_info(idx.name) AS col WHERE idx."unique" = 1 AND col.name = 'c_series_tag'`).Scan(&chunkUnique).Error; err != nil {
			return fmt.Errorf("检查表结构 t_collector_kline_backfill_chunks: %w", err)
		}
		if chunkUnique == 0 {
			problems = append(problems, "t_collector_kline_backfill_chunks 的唯一键不含 c_series_tag")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w：%s。请先停止 Collector 并备份数据库，再用 sqlite3 -bail 执行发布包里 collector/schema/migrations/20261105-unified-dataset.sql 升级（只能对未升级的库执行一次，步骤见《采集》文档“升级已有的 Collector 库”），然后再启动", ErrSchemaNotUpgraded, strings.Join(problems, "；"))
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
