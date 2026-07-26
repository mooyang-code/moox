package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	coremetadata "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	_ "modernc.org/sqlite"
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
	db, err := sql.Open("sqlite", withPragmas(opts.Path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, schemaPath: opts.SchemaPath, now: time.Now}, nil
}

func withPragmas(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) InitSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	if s.schemaPath == "" {
		return errors.New("metadata schema path is required for schema initialization")
	}
	if err := s.checkSchemaVersion(ctx); err != nil {
		return err
	}
	schema, err := os.ReadFile(s.schemaPath)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, string(schema))
	return err
}

// ValidateSchemaVersion checks that the persisted metadata database matches the
// schema shipped with the current binary without creating or altering tables.
func (s *Store) ValidateSchemaVersion(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("metadata store is not open")
	}
	if err := s.checkSchemaVersion(ctx); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_schema_meta'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("storage metadata schema is not initialized")
	}
	return nil
}

const metadataSchemaVersion = "5"

func (s *Store) checkSchemaVersion(ctx context.Context) error {
	var schemaTableCount int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM sqlite_master
		WHERE type = 'table' AND name = 't_schema_meta'
	`).Scan(&schemaTableCount); err != nil {
		return err
	}
	if schemaTableCount == 0 {
		var existingTables int
		if err := s.db.QueryRowContext(ctx, `
			SELECT COUNT(1) FROM sqlite_master
			WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		`).Scan(&existingTables); err != nil {
			return err
		}
		if existingTables > 0 {
			return errors.New("incompatible storage metadata schema; reset metadata database")
		}
		return nil
	}
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && version != metadataSchemaVersion) {
		return errors.New("incompatible storage metadata schema; reset metadata database")
	}
	return err
}

func (s *Store) nowUTC() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) TableNames(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
