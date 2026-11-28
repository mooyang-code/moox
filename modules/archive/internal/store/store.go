// Package store persists archive tasks, runs, partitions and the worker heartbeat in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"
	archiveschema "github.com/mooyang-code/moox/modules/archive/schema"
)

var (
	// ErrNotFound 表示记录不存在。
	ErrNotFound = errors.New("archive record not found")
	// ErrConflict 表示唯一性或状态冲突（例如数据集已有任务、运行已结束）。
	ErrConflict = errors.New("archive store conflict")
	// ErrHasPartitions 表示任务已有分区登记，不能删除。
	ErrHasPartitions = errors.New("archive task has partitions")
)

// DirNameTakenError 表示目录名已被同空间同用途的另一个任务使用。
type DirNameTakenError struct{ DirName, DatasetID string }

func (e *DirNameTakenError) Error() string {
	return fmt.Sprintf("目录名 %s 已被 %s 使用，请改一个", e.DirName, e.DatasetID)
}

const (
	StatusEnabled = "enabled"
	StatusPaused  = "paused"

	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCanceled  = "canceled"

	// KindFollow 是 mgr 每分钟生成的跟随运行（按 Storage 变更日志读改动过的行并导出定稿）；
	// KindManual 是整段重读线上保留期；KindRange 是补拉区间；
	// KindImport 是 moox-cli import 的归档目标（经 worker 数据端口写入暂存区，不推进位点、不更新最近成功时间）。
	KindFollow = "follow"
	KindManual = "manual"
	KindRange  = "range"
	KindImport = "import"

	PartitionOpen     = "open"
	PartitionExported = "exported"
	PartitionFinal    = "final"

	// BlockedReasonSourceDatasetMissing 复用 t_archive_tasks.c_blocked_reason 标记源数据集在 Storage 中不存在：
	// 不修改任务启停状态 c_status，只暂停生成 follow 运行；数据集重新出现时由维护刷新自动清除。
	BlockedReasonSourceDatasetMissing = "源数据集不存在"
)

// Store 持有 mgr 的 SQLite 连接。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）SQLite 库。新库以 auto_vacuum = INCREMENTAL 创建。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("archive database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create archive database directory: %w", err)
	}
	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open archive database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	if err := db.Ping(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("open archive database: %w", err)
	}
	// auto_vacuum 只对尚未建表的新库生效，必须在建表之前设置。
	if _, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("set auto_vacuum: %w", err)
	}
	if _, err := db.Exec(archiveschema.AllSQL()); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("initialize archive schema: %w", err)
	}
	return s, nil
}

func buildDSN(path string) string {
	// auto_vacuum 只对尚未建表的新库生效，必须排在 journal_mode(WAL) 之前。
	pragmas := []string{
		"_pragma=auto_vacuum(2)",
		"_pragma=foreign_keys(ON)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=temp_store(MEMORY)",
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + strings.Join(pragmas, "&")
}

// Ping 检查数据库可用。
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("archive database is not open")
	}
	return s.db.PingContext(ctx)
}

// Close 释放连接。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// IncrementalVacuum 回收空闲页。
func (s *Store) IncrementalVacuum(ctx context.Context) error {
	// incremental_vacuum 每一步只回收一页，Exec 只执行一步，要用 Query 把结果读完。
	rows, err := s.db.QueryContext(ctx, "PRAGMA incremental_vacuum")
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	return errors.Join(rows.Err(), rows.Close())
}

// tx 在事务里执行 fn；返回错误即回滚。
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "UNIQUE constraint failed") || strings.Contains(text, "constraint failed: UNIQUE")
}

// ---- 时间列（Unix 毫秒整数，0 表示没有） ----

func millisOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixMilli()
}

// timeValue 扫描 Unix 毫秒整数时间列；0 或 NULL 表示零值时间。
type timeValue struct{ time.Time }

func (v *timeValue) Scan(src any) error {
	switch value := src.(type) {
	case nil:
		v.Time = time.Time{}
	case int64:
		if value == 0 {
			v.Time = time.Time{}
		} else {
			v.Time = time.UnixMilli(value).UTC()
		}
	default:
		return fmt.Errorf("unsupported time column type %T", src)
	}
	return nil
}

func newID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(raw[:])
}

func newToken() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}
