// Package database 提供 Trade 模块的 SQLite 持久化管理。
//
// Trade 模块账户域与交易域共用同一 SQLite 文件，启动时通过
// schema.AllSQL() 一次性建表。DSN 带 WAL/busy_timeout 等 pragma，
// 与 admin 的 SQLite 配置风格一致。
package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager 数据库管理器。
type Manager struct {
	db *gorm.DB
}

// NewManager 创建数据库管理器。
func NewManager() *Manager { return &Manager{} }

// Initialize 打开 SQLite 并应用 schema（建表）。
func (dm *Manager) Initialize(dbCfg *config.DatabaseConfig) error {
	dbPath := "./data/moox_trade.db"
	if dbCfg != nil && dbCfg.Path != "" {
		dbPath = dbCfg.Path
	}
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(buildSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	dm.db = db
	applySQLitePoolConfig(dm.db, dbCfg)
	if err := dm.ApplySchemaSQL("embedded trade schema", tradeschema.AllSQL()); err != nil {
		return err
	}
	log.Infof("初始化Trade SQLite数据库: %s", dbPath)
	return nil
}

// ApplySchemaSQL 应用给定 SQL 文本（建表/索引/触发器）。
func (dm *Manager) ApplySchemaSQL(name, raw string) error {
	if dm.db == nil {
		return fmt.Errorf("database is not initialized")
	}
	if err := dm.db.Exec(raw).Error; err != nil {
		return fmt.Errorf("apply schema %s: %w", name, err)
	}
	return nil
}

// GetDB 返回底层 gorm 连接，供 DAO 使用。
func (dm *Manager) GetDB() *gorm.DB { return dm.db }

// Close 关闭数据库（GORM SQLite 由 sql.DB 管理生命周期，这里显式关闭）。
func (dm *Manager) Close() error {
	if dm.db == nil {
		return nil
	}
	sqlDB, err := dm.db.DB()
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
	maxOpen, maxIdle := 30, 20
	if cfg != nil {
		if cfg.MaxOpenConns > 0 {
			maxOpen = cfg.MaxOpenConns
		}
		if cfg.MaxIdleConns > 0 {
			if cfg.MaxIdleConns < maxOpen {
				maxIdle = cfg.MaxIdleConns
			}
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
