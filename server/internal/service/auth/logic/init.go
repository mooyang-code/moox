package logic

import (
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/auth/config"
	"github.com/mooyang-code/moox/server/internal/service/auth/dao"
	"trpc.group/trpc-go/trpc-go/log"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// AuthServiceImpl 认证服务实现
type AuthServiceImpl struct {
	cfg     *config.Config
	userDAO *dao.UserDAO
}

// InitAuthServiceImpl 初始化认证服务实现
func InitAuthServiceImpl(cfg *config.Config) (*AuthServiceImpl, error) {
	imp := &AuthServiceImpl{
		cfg: cfg,
	}

	// 初始化数据库连接
	db, err := imp.initDB()
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}

	// 初始化缓存
	cache, err := imp.initCache()
	if err != nil {
		return nil, fmt.Errorf("初始化缓存失败: %w", err)
	}

	// 初始化DAO
	imp.userDAO = dao.NewUserDAO(db, cache)

	// 自动迁移表结构
	if err := imp.userDAO.AutoMigrate(); err != nil {
		log.Warnf("自动迁移表结构失败: %v", err)
	}
	return imp, nil
}

// initDB 初始化数据库连接
func (s *AuthServiceImpl) initDB() (*gorm.DB, error) {
	// 使用SQLite数据库
	dsn := s.cfg.Database.DBName
	if dsn == "" {
		dsn = "auth.db"
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	log.Infof("初始化SQLite数据库连接: %s", dsn)
	return db, nil
}

// initCache 初始化缓存
func (s *AuthServiceImpl) initCache() (*dao.CacheDB, error) {
	// 使用BadgerDB作为缓存
	cacheDir := s.cfg.Cache.DataDir
	if cacheDir == "" {
		cacheDir = "./cache"
	}

	cache, err := dao.NewCacheDB(cacheDir)
	if err != nil {
		return nil, err
	}

	log.Infof("初始化BadgerDB缓存: %s", cacheDir)
	return cache, nil
}
