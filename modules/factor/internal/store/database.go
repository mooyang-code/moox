// Package store persists Factor configuration and asynchronous recalc jobs.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
)

var ErrConflict = errors.New("factor store conflict")

// Store owns the Factor SQLite connection.
type Store struct {
	db   *gorm.DB
	path string
}

// SQLDB 返回底层数据库连接（测试直接改写数据时用）。
func (s *Store) SQLDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("factor store is not open")
	}
	return s.db.DB()
}

// Options configures the Factor SQLite store.
type Options struct {
	Path            string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open 打开 Factor SQLite 数据库，应用并校验当前 schema，通过后写出格式文件。
func Open(opts *Options) (*Store, error) {
	dbPath := "./data/factor/factor.db"
	if opts != nil && opts.Path != "" {
		dbPath = opts.Path
	}
	dbOpts := sqlitedb.Options{
		Path:         dbPath,
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	}
	if opts != nil {
		if opts.MaxOpenConns > 0 {
			dbOpts.MaxOpenConns = opts.MaxOpenConns
		}
		if opts.MaxIdleConns > 0 {
			dbOpts.MaxIdleConns = opts.MaxIdleConns
		}
		dbOpts.ConnMaxLifetime = opts.ConnMaxLifetime
		dbOpts.ConnMaxIdleTime = opts.ConnMaxIdleTime
	}
	db, err := sqlitedb.OpenGORM(dbOpts)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db, path: dbPath}
	if err := s.ApplySchema(factorschema.AllSQL()); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("initialize factor schema: %w", err)
	}
	return s, nil
}

// ApplySchema 应用内嵌 schema 并与内存参考库严格比对，通过后把格式记录写入数据目录。
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("factor schema sql is empty")
	}
	return sqlitedb.EnsureSchemaGORM(s.db, s.path, "factor-mgr", sql, nil)
}

// WithTx runs fn inside a Factor SQLite transaction.
func (s *Store) WithTx(ctx context.Context, fn func(*gorm.DB) error) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if fn == nil {
		return fmt.Errorf("factor transaction callback is required")
	}
	return s.db.WithContext(ctx).Transaction(fn)
}

// Ping verifies that the database is available.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
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
