package auth

import (
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/config"
	authimpl "github.com/mooyang-code/moox/modules/admin/internal/service/auth/impl"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// Service 认证服务接口
type Service interface {
	pb.AuthService // 继承protobuf生成的认证API服务接口
}

// NewService 新建认证服务；sessions 是与控制台共用的浏览器会话表。
func NewService(cfg *config.Config, dbManager *database.Manager, sessions *session.Store) (Service, error) {
	return authimpl.InitAuthServiceImpl(cfg, dbManager, sessions)
}
