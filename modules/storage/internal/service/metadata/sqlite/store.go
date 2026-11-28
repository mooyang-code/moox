package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	storageschema "github.com/mooyang-code/moox/modules/storage/schema"
	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/sqlitedb"
)

var _ coremetadata.Store = (*Store)(nil)

type readSnapshotContextKey struct{}

// queryDB keeps all metadata reads in one transaction while the cache is
// rebuilding. Normal requests continue to use the long-lived database handle.
func (s *Store) queryDB(ctx context.Context) readDB {
	if tx, ok := ctx.Value(readSnapshotContextKey{}).(*sql.Tx); ok {
		return tx
	}
	return s.db
}

// WithReadSnapshot executes a cache refresh against one SQLite read snapshot.
// It is intentionally optional so non-SQLite metadata readers keep working.
func (s *Store) WithReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	readCtx := context.WithValue(ctx, readSnapshotContextKey{}, tx)
	if err := fn(readCtx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Options 保存 SQLite 元数据存储打开配置。
type Options struct {
	Path       string
	SchemaPath string
}

// Store 封装 SQLite 元数据表的直接读写能力。
type Store struct {
	db         *sql.DB
	schemaPath string
	now        func() time.Time
}

func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("metadata sqlite path is required")
	}
	db, err := sqlitedb.OpenSQL(sqlitedb.Options{
		Path:          opts.Path,
		BusyTimeoutMS: 5000,
	})
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, schemaPath: opts.SchemaPath, now: time.Now}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) schemaSQL() (string, error) {
	if strings.TrimSpace(s.schemaPath) == "" {
		return storageschema.AllSQL(), nil
	}
	raw, err := os.ReadFile(s.schemaPath)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (s *Store) InitSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	schemaText, err := s.schemaSQL()
	if err != nil {
		return err
	}
	return sqlitedb.ApplySchemaSQL(ctx, s.db, "storage-primary", schemaText)
}

// ValidateSchema 严格比对持久化元数据库与当前二进制内嵌的 schema（不建表、不改表）。
func (s *Store) ValidateSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	return sqlitedb.ValidateSQL(ctx, s.db, "storage-primary", storageschema.AllSQL(), false)
}

// RecordPrimaryFormat 在元数据库与 Pebble 布局都校验通过后，把 storage-primary 的复合格式写入数据目录。
func RecordPrimaryFormat(storageRoot string) error {
	if strings.TrimSpace(storageRoot) == "" {
		return nil
	}
	eventsDigest, err := events.FormatDigest()
	if err != nil {
		return fmt.Errorf("计算事件格式摘要失败：%w", err)
	}
	return sqlitedb.RecordFormatAt(
		filepath.Join(storageRoot, dataformat.FileName),
		"storage-primary",
		storageschema.AllSQL(),
		map[string]string{
			dataformat.PartStoragePebbleLayout: strconv.Itoa(dataformat.StoragePebbleLayoutVersion),
			dataformat.PartEvents:              eventsDigest,
		},
	)
}

func (s *Store) nowUTC() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
