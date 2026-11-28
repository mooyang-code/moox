// Package store owns Collector's SQLite connection and persistence repositories.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdlog "log"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"trpc.group/trpc-go/trpc-go/log"
)

// SQLDB 返回底层数据库连接（测试直接改写运行态时用）。
func (s *Store) SQLDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("collector store is not open")
	}
	return s.db.DB()
}

// Store owns the Collector SQLite connection and repositories.
type Store struct {
	db         *gorm.DB
	path       string
	taskRepo   *TaskRepository
	fetchPlans *FetchPlanRepository
	timeline   *SeriesTimelineRepository
	workers    *StreamWorkerRepository
	exclusions *SubjectExclusionRepository
	tagRuns    *TagSyncRunRepository
	backfill   *BackfillRepository
	resample   *ResampleStateRepository
	delist     *DelistNoticeRepository
	accounts   *CloudAccountRepository
	packages   *FunctionPackageRepository
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
		if err := tx.Exec(`DELETE FROM t_collector_resample_jobs WHERE c_space_id = ? AND c_task_id = ?`, spaceID, taskID).Error; err != nil {
			return err
		}
		// 若任务有独占结果数据集，一并清理该数据集在 Collector 内的历史补齐、水位、确认缺失、补写通知与定向补采作业。
		// 标的时间线不在这里清：任务删掉后，下一份采集计划会给它的数据集写一行“停止采集”，在那之前检查与补采仍会碰这个数据集。
		var datasetID string
		if err := tx.Table("t_collector_tasks").
			Select("c_result_dataset_id").
			Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).
			Limit(1).Scan(&datasetID).Error; err != nil {
			return err
		}
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" {
			return nil
		}
		var otherRefs int64
		if err := tx.Table("t_collector_tasks").
			Where("c_space_id = ? AND c_result_dataset_id = ? AND c_task_id <> ?", spaceID, datasetID, taskID).
			Count(&otherRefs).Error; err != nil {
			return err
		}
		if otherRefs > 0 {
			return nil
		}
		for _, stmt := range []string{
			`DELETE FROM t_collector_history_seeds WHERE c_space_id = ? AND c_dataset_id = ?`,
			`DELETE FROM t_collector_backfill_watermarks WHERE c_space_id = ? AND c_dataset_id = ?`,
			`DELETE FROM t_collector_source_absences WHERE c_space_id = ? AND c_dataset_id = ?`,
			`DELETE FROM t_collector_backfill_notices WHERE c_space_id = ? AND c_dataset_id = ?`,
			`DELETE FROM t_collector_kline_backfill_chunks WHERE c_job_id IN (SELECT c_job_id FROM t_collector_kline_backfill_jobs WHERE c_space_id = ? AND c_dataset_id = ?)`,
			`DELETE FROM t_collector_kline_backfill_jobs WHERE c_space_id = ? AND c_dataset_id = ?`,
		} {
			if err := tx.Exec(stmt, spaceID, datasetID).Error; err != nil {
				return err
			}
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
	if err := s.db.WithContext(ctx).Table("t_collector_tasks").
		Where("c_space_id = ? AND c_result_dataset_id = ? AND c_task_id <> ?", spaceID, datasetID, taskID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Options configures the Collector SQLite store.
type Options struct {
	Path string
	// DefaultIngest 按空间返回 K 线任务的默认采集方式（市场 market.yaml 的 ingest），空间没有登记时返回空串。
	// 建任务没写采集方式时用它；为 nil 时每个 K 线任务都必须自己写明采集方式。
	DefaultIngest   func(spaceID string) string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

func (o *Options) defaultIngest() func(string) string {
	if o == nil {
		return nil
	}
	return o.DefaultIngest
}

// Open opens the Collector SQLite store. Schema creation is handled by bootstrap.
func Open(opts *Options) (*Store, error) {
	dbPath := "./data/moox_collector.db"
	if opts != nil && opts.Path != "" {
		dbPath = opts.Path
	}
	// 查不到记录是调用方用 ErrRecordNotFound 判断的正常结果，不能每次都当成错误打日志。
	quiet := gormlogger.New(stdlog.New(os.Stderr, "\r\n", stdlog.LstdFlags), gormlogger.Config{SlowThreshold: 200 * time.Millisecond, LogLevel: gormlogger.Warn, IgnoreRecordNotFoundError: true})
	dbOpts := sqlitedb.Options{
		Path:         dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
		Logger:       quiet,
	}
	if opts != nil {
		dbOpts.ConnMaxLifetime = opts.ConnMaxLifetime
		dbOpts.ConnMaxIdleTime = opts.ConnMaxIdleTime
	}
	db, err := sqlitedb.OpenGORM(dbOpts)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, path: dbPath}
	s.taskRepo = NewTaskRepository(db, opts.defaultIngest())
	s.fetchPlans = NewFetchPlanRepository(db)
	s.timeline = NewSeriesTimelineRepository(db)
	s.workers = NewStreamWorkerRepository(db)
	s.exclusions = NewSubjectExclusionRepository(db)
	s.tagRuns = NewTagSyncRunRepository(db)
	s.backfill = NewBackfillRepository(db)
	s.resample = NewResampleStateRepository(db)
	s.delist = NewDelistNoticeRepository(db)
	s.accounts = NewCloudAccountRepository(db)
	s.packages = NewFunctionPackageRepository(db)
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

// SeriesTimeline 返回标的时间线仓库。
func (s *Store) SeriesTimeline() *SeriesTimelineRepository {
	if s == nil {
		return nil
	}
	return s.timeline
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

// CloudAccounts 返回云账号仓库。
func (s *Store) CloudAccounts() *CloudAccountRepository {
	if s == nil {
		return nil
	}
	return s.accounts
}

// FunctionPackages 返回云函数代码包仓库。
func (s *Store) FunctionPackages() *FunctionPackageRepository {
	if s == nil {
		return nil
	}
	return s.packages
}

// Backfill returns the K-line backfill job repository.
func (s *Store) Backfill() *BackfillRepository {
	if s == nil {
		return nil
	}
	return s.backfill
}

// ApplySchema 应用并严格校验 Collector schema，通过后把格式记录写入数据目录。
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("collector database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("collector schema sql is empty")
	}
	return sqlitedb.EnsureSchemaGORM(s.db, s.path, "collector", sql, dataformat.LifecycleParts())
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
