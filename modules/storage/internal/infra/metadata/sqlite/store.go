package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sort"

	_ "modernc.org/sqlite"
)

// Options 保存 SQLite 元数据存储打开配置。
type Options struct {
	Path       string
	SchemaPath string
}

// Store 封装 SQLite 元数据表的直接读写能力。
type Store struct {
	db         *sql.DB
	schemaPath string
}

func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Path == "" {
		return nil, errors.New("metadata sqlite path is required")
	}
	db, err := sql.Open("sqlite", opts.Path)
	if err != nil {
		return nil, err
	}
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
	return &Store{db: db, schemaPath: opts.SchemaPath}, nil
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
