// Package staging is the worker's DuckDB staging area: one staged table per task plus the task
// watermark and the (permanent) partition state.
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

	_ "github.com/marcboeker/go-duckdb/v2"
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

func sqlType(t string) (string, error) {
	switch t {
	case TypeDouble:
		return "DOUBLE", nil
	case TypeInt:
		return "BIGINT", nil
	case TypeString, TypeJSON:
		return "VARCHAR", nil
	case TypeBool:
		return "BOOLEAN", nil
	case TypeTime:
		return "TIMESTAMP", nil
	case TypeBytes:
		return "BLOB", nil
	default:
		return "", fmt.Errorf("unsupported column type %q", t)
	}
}

func quote(name string) string { return `"` + name + `"` }

func literal(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

// Options 是 DuckDB 的资源限制。
type Options struct {
	MemoryLimit string
	Threads     int
}

// Store 是暂存库。
type Store struct {
	db   *sql.DB
	path string
	opts Options
}

var memoryLimitPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?\s?(B|KB|MB|GB|TB|KiB|MiB|GiB|TiB)$`)

// TempDir 返回暂存库专用的临时目录。
func TempDir(path string) string { return path + ".tmp" }

// Open 打开（必要时创建）暂存库，并创建任务水位与分区状态两张永久表。
func Open(path string, opts Options) (*Store, error) {
	if opts.MemoryLimit == "" {
		opts.MemoryLimit = "1GB"
	}
	if opts.Threads <= 0 {
		opts.Threads = 2
	}
	if !memoryLimitPattern.MatchString(opts.MemoryLimit) {
		return nil, fmt.Errorf("invalid DuckDB memory limit %q", opts.MemoryLimit)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tempDir := TempDir(path)
	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return nil, err
	}
	db, err := openDB(path, tempDir, opts)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, path: path, opts: opts}
	if _, err := db.Exec(`
CREATE TABLE IF NOT EXISTS task_state (
    space_id VARCHAR NOT NULL,
    dataset_id VARCHAR NOT NULL,
    scan_watermark TIMESTAMP,
    first_start TIMESTAMP,
    PRIMARY KEY (space_id, dataset_id)
);
CREATE TABLE IF NOT EXISTS partition_state (
    space_id VARCHAR NOT NULL,
    dataset_id VARCHAR NOT NULL,
    partition_key VARCHAR NOT NULL,
    range_start TIMESTAMP NOT NULL,
    range_end TIMESTAMP NOT NULL,
    status VARCHAR NOT NULL DEFAULT 'open',
    last_changed_at TIMESTAMP NOT NULL,
    exported_at TIMESTAMP,
    file_sha256 VARCHAR NOT NULL DEFAULT '',
    export_count INTEGER NOT NULL DEFAULT 0,
    row_count BIGINT NOT NULL DEFAULT 0,
    subject_count BIGINT NOT NULL DEFAULT 0,
    min_data_time TIMESTAMP,
    max_data_time TIMESTAMP,
    finalized_at TIMESTAMP,
    report_pending BOOLEAN NOT NULL DEFAULT TRUE,
    CHECK (status IN ('open', 'exported', 'final')),
    PRIMARY KEY (space_id, dataset_id, partition_key)
)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create staging state tables: %w", err)
	}
	// 旧库补列（IF NOT EXISTS，重复执行无害）。
	for _, ddl := range []string{
		`ALTER TABLE task_state ADD COLUMN IF NOT EXISTS first_start TIMESTAMP`,
		`ALTER TABLE task_state ADD COLUMN IF NOT EXISTS task_epoch VARCHAR`,
		`ALTER TABLE partition_state ADD COLUMN IF NOT EXISTS row_count BIGINT DEFAULT 0`,
		`ALTER TABLE partition_state ADD COLUMN IF NOT EXISTS subject_count BIGINT DEFAULT 0`,
		`ALTER TABLE partition_state ADD COLUMN IF NOT EXISTS min_data_time TIMESTAMP`,
		`ALTER TABLE partition_state ADD COLUMN IF NOT EXISTS max_data_time TIMESTAMP`,
		`ALTER TABLE partition_state ADD COLUMN IF NOT EXISTS finalized_at TIMESTAMP`,
		`ALTER TABLE partition_state ADD COLUMN IF NOT EXISTS report_pending BOOLEAN DEFAULT TRUE`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("upgrade staging state tables: %w", err)
		}
	}
	return s, nil
}

func openDB(path, tempDir string, opts Options) (*sql.DB, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, err
	}
	// 暂存库由一个写入者使用；单连接让事务与临时表在同一个会话里。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	settings := fmt.Sprintf("SET memory_limit = %s; SET threads = %d; SET temp_directory = %s", literal(opts.MemoryLimit), opts.Threads, literal(tempDir))
	if _, err := db.Exec(settings); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure DuckDB: %w", err)
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

// Checkpoint 把 WAL 合入库文件。
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, "CHECKPOINT")
	return err
}

// FileBytes 返回库文件当前大小（不含临时目录）。
func (s *Store) FileBytes() int64 {
	info, err := os.Stat(s.path)
	if err != nil {
		return 0
	}
	total := info.Size()
	if wal, err := os.Stat(s.path + ".wal"); err == nil {
		total += wal.Size()
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
	create := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (subject_id VARCHAR NOT NULL, data_time TIMESTAMP NOT NULL, series_tag VARCHAR NOT NULL%s, PRIMARY KEY (subject_id, data_time, series_tag))`, quote(task.Table()), extra)
	if _, err := s.db.ExecContext(ctx, create); err != nil {
		return fmt.Errorf("create staged table: %w", err)
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
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, quote(task.Table()), quote(c.Name), typ)); err != nil {
			return fmt.Errorf("add staged column %s: %w", c.Name, err)
		}
	}
	return nil
}

func (s *Store) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = ?`, table)
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
	rows, err := s.db.QueryContext(ctx, `SELECT table_name FROM information_schema.tables WHERE table_name LIKE 'staged\_%' ESCAPE '\'`)
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
	var w sql.NullTime
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
	return w.Time.UTC(), true, nil
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
