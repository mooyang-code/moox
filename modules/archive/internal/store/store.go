// Package store persists archive tasks, runs, partitions and worker leases in SQLite.
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

	"github.com/glebarez/sqlite"
	archiveschema "github.com/mooyang-code/moox/modules/archive/schema"
	"gorm.io/gorm"
)

var (
	// ErrNotFound 表示记录不存在。
	ErrNotFound = errors.New("archive record not found")
	// ErrConflict 表示唯一性或状态冲突（例如数据集已有任务、重复的 worker 实例）。
	ErrConflict = errors.New("archive store conflict")
	// ErrHasPartitions 表示任务已有分区登记，不能删除。
	ErrHasPartitions = errors.New("archive task has partitions")
)

// timeLayout 是库内统一的时间文本格式（UTC，定宽，字典序即时间序）。
const timeLayout = "2006-01-02 15:04:05.000"

const (
	StatusEnabled = "enabled"
	StatusPaused  = "paused"

	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCanceled  = "canceled"

	KindScheduled = "scheduled"
	KindManual    = "manual"
	KindRange     = "range"

	PartitionOpen     = "open"
	PartitionExported = "exported"
	PartitionFinal    = "final"
)

// Store 持有 mgr 的 SQLite 连接。
type Store struct {
	gdb *gorm.DB
	db  *sql.DB
}

// SQLDB 返回底层数据库连接（重置时备份与清表用，由生命周期的重置实现调用）。
func (s *Store) SQLDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("archive store is not open")
	}
	return s.db, nil
}

// Open 打开（必要时创建）SQLite 库。新库以 auto_vacuum = INCREMENTAL 创建。
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("archive database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create archive database directory: %w", err)
	}
	gdb, err := gorm.Open(sqlite.Open(buildDSN(path)), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open archive database: %w", err)
	}
	db, err := gdb.DB()
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{gdb: gdb, db: db}
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
	_, err := s.db.ExecContext(ctx, "PRAGMA incremental_vacuum")
	return err
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

// ---- 时间列 ----

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// nullTime 把零值时间写成 NULL。
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}

// timeValue 扫描可空的时间列；零值表示 NULL。驱动可能把 DATETIME 列解析成 time.Time，也可能给出文本。
type timeValue struct{ time.Time }

func (v *timeValue) Scan(src any) error {
	switch value := src.(type) {
	case nil:
		v.Time = time.Time{}
	case time.Time:
		v.Time = value.UTC()
	case string:
		return v.parse(value)
	case []byte:
		return v.parse(string(value))
	default:
		return fmt.Errorf("unsupported time column type %T", src)
	}
	return nil
}

func (v *timeValue) parse(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		v.Time = time.Time{}
		return nil
	}
	for _, layout := range []string{timeLayout, "2006-01-02 15:04:05", time.RFC3339Nano, "2006-01-02T15:04:05Z"} {
		if parsed, err := time.ParseInLocation(layout, text, time.UTC); err == nil {
			v.Time = parsed.UTC()
			return nil
		}
	}
	return fmt.Errorf("invalid time %q", text)
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
