// Package storage provides SQLite persistence for the collector control plane.
package control

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager owns the collector SQLite connection.
type Manager struct {
	db *gorm.DB
}

// NewManager creates a persistence manager.
func NewManager() *Manager {
	return &Manager{}
}

// Initialize opens SQLite and applies the embedded Collector schema.
func (m *Manager) Initialize(dbCfg *DatabaseConfig) error {
	dbPath := "./data/moox_collector.db"
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
	if err := m.applySchemaSQL("embedded collector schema", collectorschema.AllSQL()); err != nil {
		return err
	}
	log.Infof("初始化 Collector SQLite 数据库: %s", dbPath)
	return nil
}

// applySchemaSQL applies the given schema text.
func (m *Manager) applySchemaSQL(name string, raw string) error {
	if m.db == nil {
		return fmt.Errorf("database is not initialized")
	}
	if err := m.db.Exec(raw).Error; err != nil {
		return fmt.Errorf("apply schema %s: %w", name, err)
	}
	return nil
}

// DB returns the raw gorm connection.
func (m *Manager) DB() *gorm.DB {
	return m.db
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

func applySQLitePoolConfig(db *gorm.DB, cfg *DatabaseConfig) {
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
