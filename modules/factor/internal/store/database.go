// Package store owns Factor's SQLite connection and persistence repositories.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Store owns the Factor SQLite connection and repositories.
type Store struct {
	db       *gorm.DB
	factors  *FactorRepository
	bindings *BindingRepository
}

// Options configures the Factor SQLite store.
type Options struct {
	Path            string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open opens the Factor SQLite store. Schema creation is handled by bootstrap.
func Open(opts *Options) (*Store, error) {
	dbPath := "./data/factor/factor.db"
	if opts != nil && opts.Path != "" {
		dbPath = opts.Path
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{db: db}
	s.factors = NewFactorRepository(db)
	s.bindings = NewBindingRepository(db)
	applySQLitePoolConfig(db, opts)
	log.Infof("初始化 Factor SQLite 数据库: %s", dbPath)
	return s, nil
}

// Factors returns the factor repository.
func (s *Store) Factors() *FactorRepository {
	if s == nil {
		return nil
	}
	return s.factors
}

// Bindings returns the factor binding repository.
func (s *Store) Bindings() *BindingRepository {
	if s == nil {
		return nil
	}
	return s.bindings
}

// ApplySchema applies schema SQL during service startup.
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("factor schema sql is empty")
	}
	return s.db.Exec(sql).Error
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

func buildSQLiteDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=busy_timeout(5000)",
		"_pragma=temp_store(MEMORY)",
		"_pragma=cache_size(-64000)",
		"_pragma=wal_autocheckpoint(1000)",
	}
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + strings.Join(pragmas, "&")
}

func applySQLitePoolConfig(db *gorm.DB, cfg *Options) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	maxOpen := 30
	maxIdle := 20
	if cfg != nil {
		if cfg.MaxOpenConns > 0 {
			maxOpen = cfg.MaxOpenConns
		}
		if cfg.MaxIdleConns > 0 && cfg.MaxIdleConns < maxOpen {
			maxIdle = cfg.MaxIdleConns
		}
		if cfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime > 0 {
			sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
}
