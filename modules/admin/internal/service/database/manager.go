package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/config"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// Manager 数据库管理器，负责数据库初始化和连接管理
type Manager struct {
	db    *gorm.DB
	cache *badger.DB
}

// NewManager 创建数据库管理器
func NewManager() *Manager {
	return &Manager{}
}

// Initialize 初始化数据库连接
func (dm *Manager) Initialize(dbCfg *config.DatabaseConfig) error {
	dbPath := "./data/moox.db"
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
	if err := dm.ApplyAdminSchema(); err != nil {
		return err
	}
	log.Infof("初始化SQLite数据库连接: %s", dbPath)
	return nil
}

// ApplyAdminSchema 应用 Control/Admin schema。
//
// 默认使用编译进二进制的 schema，避免部署时依赖工作目录或源码路径。
// 如确需临时覆盖，可通过 MOOX_CONTROL_ADMIN_SCHEMA_FILE 指向外部 SQL 文件。
func (dm *Manager) ApplyAdminSchema() error {
	if path := strings.TrimSpace(os.Getenv("MOOX_CONTROL_ADMIN_SCHEMA_FILE")); path != "" {
		return dm.ApplySchema(path)
	}
	return dm.ApplySchemaSQL("embedded admin.sql", adminSchemaSQL())
}

// ApplySchema 应用 Control/Admin 的权威 SQL schema。
func (dm *Manager) ApplySchema(schemaPath string) error {
	if dm.db == nil {
		return fmt.Errorf("database is not initialized")
	}
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema %s: %w", schemaPath, err)
	}
	return dm.ApplySchemaSQL(schemaPath, string(raw))
}

// ApplySchemaSQL 应用给定 SQL 文本。
func (dm *Manager) ApplySchemaSQL(schemaName string, raw string) error {
	if dm.db == nil {
		return fmt.Errorf("database is not initialized")
	}
	if err := dm.db.Exec(raw).Error; err != nil {
		return fmt.Errorf("apply schema %s: %w", schemaName, err)
	}
	return nil
}

// InitializeCache 初始化缓存（BadgerDB）
func (dm *Manager) InitializeCache(cacheDir string) error {
	if cacheDir == "" {
		cacheDir = "./data/cache"
	}

	// 确保目录存在
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	opts := badger.DefaultOptions(cacheDir)
	opts.Logger = nil // 禁用 BadgerDB 的默认日志

	cache, err := badger.Open(opts)
	if err != nil {
		return fmt.Errorf("failed to open cache: %w", err)
	}

	dm.cache = cache
	log.Infof("初始化BadgerDB缓存: %s", cacheDir)
	return nil
}

// GetDB 获取数据库连接（仅供需要原始db的API层使用）
// 注意：业务逻辑应通过DAO访问数据库，避免直接使用此方法
func (dm *Manager) GetDB() *gorm.DB {
	return dm.db
}

// GetCache 获取缓存连接
func (dm *Manager) GetCache() *badger.DB {
	return dm.cache
}

// CreateInstance 创建新的数据库实例（用于某些需要独立连接的场景）
func (dm *Manager) CreateInstance() *gorm.DB {
	return dm.db
}

// Close 关闭数据库连接和缓存
func (dm *Manager) Close() error {
	if dm.cache != nil {
		if err := dm.cache.Close(); err != nil {
			log.Errorf("关闭缓存失败: %v", err)
			return err
		}
	}
	// GORM SQLite 不需要手动关闭
	return nil
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

	maxOpen := 30
	maxIdle := 20
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

func adminSchemaSQL() string {
	return adminschema.AdminSQL()
}
