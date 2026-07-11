// Package database 提供 Trade 模块的 SQLite 持久化管理。
//
// Trade 模块账户域与交易域共用同一 SQLite 文件。Schema 初始化由服务启动前
// 的独立流程保证；本包只负责打开连接和配置 SQLite pragma。
package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager 数据库管理器。
type Manager struct {
	db *gorm.DB
}

// NewManager 创建数据库管理器。
func NewManager() *Manager { return &Manager{} }

// Initialize 打开 SQLite。表结构由服务启动前的独立 schema 初始化流程保证。
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
	log.Infof("初始化Trade SQLite数据库: %s", dbPath)
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
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(ON)",
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
