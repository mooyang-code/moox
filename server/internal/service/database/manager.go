package database

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dgraph-io/badger/v4"
	"github.com/glebarez/sqlite"
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
func (dm *Manager) Initialize(dbPath string) error {
	if dbPath == "" {
		dbPath = "./data/moox.db"
	}

	// 确保目录存在
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	dm.db = db
	log.Infof("初始化SQLite数据库连接: %s", dbPath)
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
