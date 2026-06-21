package impl

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/control/internal/service/auth/config"
	"github.com/mooyang-code/moox/modules/control/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/control/internal/service/database"
)

// AuthServiceImpl 认证服务实现
type AuthServiceImpl struct {
	cfg     *config.Config
	userDAO *dao.UserDAO
}

// InitAuthServiceImpl 初始化认证服务实现
func InitAuthServiceImpl(cfg *config.Config, dbManager *database.Manager) (*AuthServiceImpl, error) {
	imp := &AuthServiceImpl{
		cfg: cfg,
	}

	// 获取数据库连接
	db := dbManager.GetDB()
	if db == nil {
		return nil, fmt.Errorf("数据库连接未初始化")
	}

	// 初始化缓存（如果还未初始化）
	if dbManager.GetCache() == nil {
		cacheDir := cfg.Cache.DataDir
		if cacheDir == "" {
			cacheDir = "./data/cache"
		}
		if err := dbManager.InitializeCache(cacheDir); err != nil {
			return nil, fmt.Errorf("初始化缓存失败: %w", err)
		}
	}

	// 创建 CacheDB 包装器
	cache, err := dao.NewCacheDBFromBadger(dbManager.GetCache())
	if err != nil {
		return nil, fmt.Errorf("创建缓存包装器失败: %w", err)
	}

	// 初始化DAO
	imp.userDAO = dao.NewUserDAO(db, cache)
	return imp, nil
}
