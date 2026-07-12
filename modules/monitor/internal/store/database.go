// Package store owns monitor's SQLite connection and persistence repositories.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open monitor database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open sql database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)
	if err := db.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if err := db.Exec("PRAGMA busy_timeout=5000").Error; err != nil {
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	return &Store{db: db}, nil
}

func OpenFromConfig(cfg config.DatabaseConfig) (*Store, error) {
	mgr, err := Open(cfg.Path)
	if err != nil {
		return nil, err
	}
	sqlDB, err := mgr.db.DB()
	if err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("open sql database: %w", err)
	}
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	if cfg.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}
	return mgr, nil
}

// Repositories builds the monitor control-plane repository graph.
func (m *Store) Repositories() *Repositories {
	if m == nil {
		return nil
	}
	return NewRepositories(m.db)
}

// WithDatabase runs a short-lived construction callback against the private
// database handle. Production callers should prefer Repositories or domain
// methods; this escape hatch is reserved for bounded-context adapters.
func WithDatabase[T any](m *Store, fn func(*gorm.DB) T) (T, error) {
	var zero T
	if m == nil || m.db == nil {
		return zero, fmt.Errorf("monitor database is not open")
	}
	if fn == nil {
		return zero, fmt.Errorf("database callback is nil")
	}
	return fn(m.db), nil
}

// Ping verifies that the database is available.
func (m *Store) Ping(ctx context.Context) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	return m.db.WithContext(ctx).Exec("SELECT 1").Error
}

func (m *Store) ApplySchema(sql string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("monitor database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("schema sql is empty")
	}
	return m.db.Exec(sql).Error
}

func (m *Store) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
