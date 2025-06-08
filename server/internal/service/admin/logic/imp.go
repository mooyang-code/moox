package logic

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/admin/config"
	authconfig "github.com/mooyang-code/moox/server/internal/service/auth/config"
	"github.com/mooyang-code/moox/server/internal/service/auth/logic"
	pb "github.com/mooyang-code/moox/server/proto/gen"
)

// AdminServiceImpl 管理员服务实现
type AdminServiceImpl struct {
	cfg         *config.Config
	authService *logic.AuthServiceImpl
}

// InitAdminServiceImpl 初始化管理员服务实现
func InitAdminServiceImpl(cfg *config.Config) (*AdminServiceImpl, error) {
	// 创建认证服务配置
	authCfg := &authconfig.Config{
		Database: authconfig.DatabaseConfig{
			Host:     cfg.Database.Host,
			Port:     cfg.Database.Port,
			User:     cfg.Database.User,
			Password: cfg.Database.Password,
			DBName:   cfg.Database.DBName,
			SSLMode:  cfg.Database.SSLMode,
		},
		Cache: authconfig.CacheConfig{
			DataDir:  cfg.Cache.DataDir,
			Password: cfg.Cache.Password,
			DB:       cfg.Cache.DB,
		},
		JWT: authconfig.JWTConfig{
			SecretKey:     cfg.JWT.SecretKey,
			AccessExpired: cfg.JWT.AccessExpired,
		},
		Security: authconfig.SecurityConfig{
			SaltExpired:     5 * time.Minute,
			MaxLoginAttempt: 5,
			LockDuration:    30 * time.Minute,
		},
	}

	// 初始化认证服务
	authService, err := logic.InitAuthServiceImpl(authCfg)
	if err != nil {
		return nil, err
	}

	return &AdminServiceImpl{
		cfg:         cfg,
		authService: authService,
	}, nil
}

// AdminLogin 管理员登录（目前复用用户登录逻辑）
func (s *AdminServiceImpl) AdminLogin(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	// 直接调用用户登录逻辑
	// 后续可以在这里添加管理员特有的验证逻辑
	return s.authService.Login(ctx, req)
}
