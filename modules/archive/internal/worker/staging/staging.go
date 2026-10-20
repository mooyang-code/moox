// Package staging is the worker's SQLite staging area: one staged table per task plus the task
// watermark and the (permanent) partition state. 纯 Go 实现（modernc SQLite），不需要 cgo。
//
// 时间一律存 Unix 纳秒整数：Storage 的行键保留纳秒，降精度会让同一标的、同一序列相差不到 1 毫秒的两行互相覆盖。
// 转换集中在 nanos / fromNanos / nullNanos / scanNanos。
package staging

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "github.com/glebarez/go-sqlite"
)

// 列类型：与 Storage 的 FieldValueType 对应。
const (
	TypeDouble = "double"
	TypeInt    = "int"
	TypeString = "string"
	TypeBool   = "bool"
	TypeTime   = "time"
	TypeJSON   = "json"
	TypeBytes  = "bytes"
)

// 分区状态。
const (
	StatusOpen     = "open"
	StatusExported = "exported"
	StatusFinal    = "final"
)

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// reservedColumns 是暂存表自带的列，不能作为数据集的值列。
var reservedColumns = map[string]bool{"subject_id": true, "data_time": true, "series_tag": true, "freq": true}

// Column 是数据集的一个值列。
type Column struct {
	Name string
	Type string
}

// Task 是暂存区里一个归档任务的身份与当前列。
type Task struct {
	SpaceID   string
	DatasetID string
	Columns   []Column
}

// Table 返回任务的暂存表名：staged_<数据集身份的短哈希>。
func (t Task) Table() string {
	sum := sha256.Sum256([]byte(t.SpaceID + "\x00" + t.DatasetID))
	return "staged_" + hex.EncodeToString(sum[:])[:12]
}

// Validate 检查列名和类型。
func (t Task) Validate() error {
	if t.SpaceID == "" || t.DatasetID == "" {
		return errors.New("staging task needs space and dataset")
	}
	seen := map[string]bool{}
	for _, c := range t.Columns {
		if !identifier.MatchString(c.Name) || reservedColumns[c.Name] {
			return fmt.Errorf("invalid value column name %q", c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate value column %q", c.Name)
		}
		seen[c.Name] = true
		if _, err := sqlType(c.Type); err != nil {
			return err
		}
	}
	return nil
}

// sqlType 返回列在 SQLite 里的类型（决定列亲和性）：布尔存 0/1，时间存 Unix 纳秒。
func sqlType(t string) (string, error) {
	switch t {
	case TypeDouble:
		return "REAL", nil
	case TypeInt, TypeBool, TypeTime:
		return "INTEGER", nil
	case TypeString, TypeJSON:
		return "TEXT", nil
	case TypeBytes:
		return "BLOB", nil
	default:
		return "", fmt.Errorf("unsupported column type %q", t)
	}
}

func quote(name string) string { return `"` + name + `"` }

// nanos、fromNanos、nullNanos 是时间列与纳秒整数之间的转换；零值时间对应 SQL NULL。
func nanos(t time.Time) int64 { return t.UTC().UnixNano() }

func fromNanos(n int64) time.Time { return time.Unix(0, n).UTC() }

func nullNanos(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return nanos(t)
}

func scanNanos(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromNanos(v.Int64)
}

// bindValue 把行里的 Go 值转换成绑定参数：时间转纳秒、布尔转 0/1，其余原样。
func bindValue(columnType string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch columnType {
	case TypeTime:
		if t, ok := value.(time.Time); ok {
			return nanos(t), nil
		}
	case TypeBool:
		if b, ok := value.(bool); ok {
			if b {
				return int64(1), nil
			}
			return int64(0), nil
		}
	case TypeDouble:
		switch v := value.(type) {
		case float64:
			return v, nil
		case int64:
			return float64(v), nil
		}
	case TypeInt:
		if v, ok := value.(int64); ok {
			return v, nil
		}
	case TypeString, TypeJSON:
		if v, ok := value.(string); ok {
			return v, nil
		}
	case TypeBytes:
		if v, ok := value.([]byte); ok {
			return v, nil
		}
	}
	return nil, fmt.Errorf("value %T does not match column type %s", value, columnType)
}

// Options 是 SQLite 的资源限制。
type Options struct {
	// CacheBytes 是页缓存上限（PRAGMA cache_size），默认 256 MiB。
	CacheBytes int64
}

// Store 是暂存库。
type Store struct {
	db   *sql.DB
	path string
	opts Options
}

// Open 打开（必要时创建）暂存库，并创建任务水位与分区状态两张永久表。
func Open(path string, opts Options) (*Store, error) {
	if opts.CacheBytes == 0 {
		opts.CacheBytes = 256 << 20
	}
	if opts.CacheBytes < 1<<20 {
		return nil, fmt.Errorf("staging cache must be at least 1 MiB, got %d bytes", opts.CacheBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := openDB(path, opts)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, opts: opts}
	for _, ddl := range []string{`
CREATE TABLE IF NOT EXISTS task_state (
    space_id TEXT NOT NULL,
    dataset_id TEXT NOT NULL,
    scan_watermark INTEGER,
    first_start INTEGER,
    task_epoch TEXT,
    PRIMARY KEY (space_id, dataset_id)
) WITHOUT ROWID`, `
CREATE TABLE IF NOT EXISTS partition_state (
    space_id TEXT NOT NULL,
    dataset_id TEXT NOT NULL,
    partition_key TEXT NOT NULL,
    range_start INTEGER NOT NULL,
    range_end INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'open',
    last_changed_at INTEGER NOT NULL,
    exported_at INTEGER,
    file_sha256 TEXT NOT NULL DEFAULT '',
    export_count INTEGER NOT NULL DEFAULT 0,
    row_count INTEGER NOT NULL DEFAULT 0,
    subject_count INTEGER NOT NULL DEFAULT 0,
    min_data_time INTEGER,
    max_data_time INTEGER,
    finalized_at INTEGER,
    report_pending INTEGER NOT NULL DEFAULT 1,
    CHECK (status IN ('open', 'exported', 'final')),
    PRIMARY KEY (space_id, dataset_id, partition_key)
) WITHOUT ROWID`} {
		if _, err := db.Exec(ddl); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("create staging state tables: %w", err)
		}
	}
	return s, nil
}

func openDB(path string, opts Options) (*sql.DB, error) {
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=temp_store(FILE)",
		fmt.Sprintf("_pragma=cache_size(-%d)", opts.CacheBytes>>10),
	}
	db, err := sql.Open("sqlite", path+"?"+strings.Join(pragmas, "&"))
	if err != nil {
		return nil, err
	}
	// 暂存库由一个写入者使用；单连接让事务与临时表在同一个会话里。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open staging database: %w", err)
	}
	return db, nil
}

// Close 关闭暂存库。
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path 返回库文件路径。
func (s *Store) Path() string { return s.path }

// Checkpoint 把 WAL 合入库文件并截断 WAL。
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// FileBytes 返回库文件与 WAL 文件（<库>-wal）的当前大小之和。
func (s *Store) FileBytes() int64 {
	var total int64
	for _, name := range []string{s.path, s.path + "-wal"} {
		if info, err := os.Stat(name); err == nil {
			total += info.Size()
		}
	}
	return total
}

// EnsureTable 创建任务的暂存表；数据集新增列时 ALTER TABLE ADD COLUMN。
func (s *Store) EnsureTable(ctx context.Context, task Task) error {
	if err := task.Validate(); err != nil {
		return err
	}
	var defs []string
	for _, c := range task.Columns {
		typ, _ := sqlType(c.Type)
		defs = append(defs, quote(c.Name)+" "+typ)
	}
	extra := ""
	if len(defs) > 0 {
		extra = ", " + strings.Join(defs, ", ")
	}
	table := quote(task.Table())
	create := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (subject_id TEXT NOT NULL, data_time INTEGER NOT NULL, series_tag TEXT NOT NULL%s, PRIMARY KEY (subject_id, data_time, series_tag)) WITHOUT ROWID`, table, extra)
	if _, err := s.db.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("create staged table: %w", err)
	}
	// 按分区导出与统计都按 data_time 区间扫描。
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s ON %s (data_time)`, quote("idx_"+task.Table()+"_time"), table)); err != nil {
		return fmt.Errorf("create staged time index: %w", err)
	}
	have, err := s.tableColumns(ctx, task.Table())
	if err != nil {
		return err
	}
	for _, c := range task.Columns {
		if have[c.Name] {
			continue
		}
		typ, _ := sqlType(c.Type)
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, quote(c.Name), typ)); err != nil {
			return fmt.Errorf("add staged column %s: %w", c.Name, err)
		}
	}
	return nil
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// DropTask 删除任务的暂存表、水位和分区状态（任务已删除时的清理）。
func (s *Store) DropTask(ctx context.Context, spaceID, datasetID string) error {
	task := Task{SpaceID: spaceID, DatasetID: datasetID}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quote(task.Table()))); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM partition_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID)
		return err
	})
}

// StagedTables 返回暂存库里全部暂存表名。
func (s *Store) StagedTables(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'staged\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Watermark 返回任务的读取水位 W；没有时 ok 为假。
func (s *Store) Watermark(ctx context.Context, spaceID, datasetID string) (time.Time, bool, error) {
	var w sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT scan_watermark FROM task_state WHERE space_id = ? AND dataset_id = ?`, spaceID, datasetID).Scan(&w)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if !w.Valid {
		return time.Time{}, false, nil
	}
	return fromNanos(w.Int64), true, nil
}

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
