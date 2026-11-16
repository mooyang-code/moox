package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager 数据库管理器，负责数据库初始化和连接管理
type Manager struct {
	db *gorm.DB
}

// NewManager 创建数据库管理器
func NewManager() *Manager {
	return &Manager{}
}

// Initialize 初始化数据库连接
func (dm *Manager) Initialize(dbCfg *config.DatabaseConfig) error {
	dbPath := "./data/admin.db"
	if dbCfg != nil && dbCfg.Path != "" {
		dbPath = dbCfg.Path
	}

	// 确保目录存在
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	dsn := buildSQLiteDSN(dbPath)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	dm.db = db
	applySQLitePoolConfig(dm.db, dbCfg)
	log.Infof("初始化SQLite数据库连接: %s", dbPath)
	return nil
}

// GetDB 获取数据库连接（仅供需要原始db的API层使用）
// 注意：业务逻辑应通过DAO访问数据库，避免直接使用此方法
func (dm *Manager) GetDB() *gorm.DB {
	return dm.db
}

func buildSQLiteDSN(dbPath string) string {
	pragmas := []string{
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
		// Topology and encrypted signing keys must survive a committed write.
		"_pragma=synchronous(FULL)",
		// SQLite has one writer. Keep a bounded wait for short lock
		// contention, while the application-level retry handles the few
		// bookkeeping updates that race with route/health writes.
		"_pragma=busy_timeout(10000)",
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

	maxOpen := 8
	maxIdle := 4
	if cfg != nil {
		if cfg.MaxOpenConns > 0 {
			maxOpen = cfg.MaxOpenConns
		}
		if cfg.MaxIdleConns > 0 {
			maxIdle = minInt(cfg.MaxIdleConns, maxOpen)
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
