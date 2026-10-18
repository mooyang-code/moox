// Package store owns CloudNode's SQLite connection and persistence repositories.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"trpc.group/trpc-go/trpc-go/log"
)

// Store owns the CloudNode SQLite connection and repositories.
type Store struct {
	db      *gorm.DB
	catalog *CatalogRepository
}

// Open opens the CloudNode SQLite store. Schema creation is handled by bootstrap.
func Open(dbCfg *config.DatabaseConfig) (*Store, error) {
	dbPath := "./data/moox_cloudnode.db"
	if dbCfg != nil && dbCfg.Path != "" {
		dbPath = dbCfg.Path
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	// Batch Item request_json contains deployment credentials. Do not let
	// GORM's error/slow-query logger render SQL values into service logs.
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return nil, fmt.Errorf("restrict cloudnode database permissions: %w", err)
	}
	s := &Store{db: db, catalog: NewCatalogRepository(db)}
	applySQLitePoolConfig(db, dbCfg)
	log.Infof("初始化 CloudNode SQLite 数据库: %s", dbPath)
	return s, nil
}

// Catalog returns the CloudNode catalog repository.
func (s *Store) Catalog() *CatalogRepository {
	if s == nil {
		return nil
	}
	return s.catalog
}

// ApplySchema applies schema SQL during service initialization.
func (s *Store) ApplySchema(sql string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("cloudnode database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("cloudnode schema sql is empty")
	}
	return s.db.Exec(sql).Error
}

// Ping verifies that the database is available.
func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("cloudnode database is not open")
	}
	return s.db.WithContext(ctx).Exec("SELECT 1").Error
}

// Close closes the underlying SQL connection.
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
		"_pragma=synchronous(OFF)",
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

func applySQLitePoolConfig(db *gorm.DB, cfg *config.DatabaseConfig) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	if cfg != nil {
		if cfg.MaxOpenConns > 1 || cfg.MaxIdleConns > 1 {
			log.Warnf("CloudNode SQLite 强制使用单连接写入队列: configured max_open_conns=%d max_idle_conns=%d", cfg.MaxOpenConns, cfg.MaxIdleConns)
		}
		if cfg.ConnMaxLifetime > 0 {
			sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime > 0 {
			sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
}
