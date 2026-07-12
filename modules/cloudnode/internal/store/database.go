// Package store owns CloudNode's SQLite connection and persistence repositories.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager owns the cloudnode SQLite connection.
type Manager struct {
	db *gorm.DB
}

// NewManager creates a persistence manager.
func NewManager() *Manager {
	return &Manager{}
}

// Initialize opens SQLite. Schema creation is handled before service startup.
func (m *Manager) Initialize(dbCfg *config.DatabaseConfig) error {
	dbPath := "./data/moox_cloudnode.db"
	if dbCfg != nil && dbCfg.Path != "" {
		dbPath = dbCfg.Path
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	m.db = db
	applySQLitePoolConfig(m.db, dbCfg)
	log.Infof("初始化 CloudNode SQLite 数据库: %s", dbPath)
	return nil
}

// DB returns the raw gorm connection.
func (m *Manager) DB() *gorm.DB {
	return m.db
}

// Close closes the underlying SQL connection.
func (m *Manager) Close() error {
	if m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
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
