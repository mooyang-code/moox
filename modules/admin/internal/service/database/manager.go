package database

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager 数据库管理器，负责数据库初始化和连接管理。
type Manager struct {
	db *gorm.DB
}

// NewManager 创建数据库管理器。
func NewManager() *Manager {
	return &Manager{}
}

// Initialize 按统一参数打开 Admin SQLite 数据库、校验并应用 schema，通过后写出格式文件。
func (dm *Manager) Initialize(dbCfg *config.DatabaseConfig) error {
	dbPath := "./data/admin.db"
	if dbCfg != nil && dbCfg.Path != "" {
		dbPath = dbCfg.Path
	}

	opts := sqlitedb.Options{
		Path:          dbPath,
		Synchronous:   "FULL",
		BusyTimeoutMS: 10000,
		MaxOpenConns:  8,
		MaxIdleConns:  4,
	}
	if dbCfg != nil {
		if dbCfg.MaxOpenConns > 0 {
			opts.MaxOpenConns = dbCfg.MaxOpenConns
		}
		if dbCfg.MaxIdleConns > 0 {
			opts.MaxIdleConns = dbCfg.MaxIdleConns
		}
		opts.ConnMaxLifetime = dbCfg.ConnMaxLifetime
		opts.ConnMaxIdleTime = dbCfg.ConnMaxIdleTime
	}

	db, err := sqlitedb.OpenGORM(opts)
	if err != nil {
		return fmt.Errorf("打开 Admin 数据库失败：%w", err)
	}
	if err := sqlitedb.EnsureSchemaGORM(db, dbPath, "admin", adminschema.AdminSQL(), nil); err != nil {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		return fmt.Errorf("校验 Admin 数据库结构失败：%w", err)
	}

	dm.db = db
	log.Infof("初始化SQLite数据库连接: %s", dbPath)
	return nil
}

// GetDB 获取数据库连接（仅供需要原始db的API层使用）。
// 注意：业务逻辑应通过DAO访问数据库，避免直接使用此方法。
func (dm *Manager) GetDB() *gorm.DB {
	return dm.db
}
