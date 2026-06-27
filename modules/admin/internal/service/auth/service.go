package auth

import (
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	authimpl "github.com/mooyang-code/moox/modules/admin/internal/service/auth/impl"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// Service 认证服务接口
type Service interface {
	pb.AuthService // 继承protobuf生成的认证API服务接口
}

// NewService 新建认证服务
func NewService(cfg *config.Config, dbManager *database.Manager) (Service, error) {
	// 直接返回 authimpl.AuthServiceImpl，它已经实现了 pb.AuthAPIService 接口
	return authimpl.InitAuthServiceImpl(cfg, dbManager)
}
