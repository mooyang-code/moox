package auth

import (
	"github.com/mooyang-code/moox/server/internal/service/auth/config"
	"github.com/mooyang-code/moox/server/internal/service/auth/logic"
	pb "github.com/mooyang-code/moox/server/proto/gen"
)

// AuthService 认证服务总接口
type AuthService interface {
	pb.AuthAPIService
	// 可以添加其他自定义接口
}

// NewAuthService 新建认证服务
func NewAuthService(cfg *config.Config) (AuthService, error) {
	// 初始化配置
	imp, err := logic.InitAuthServiceImpl(cfg)
	if err != nil {
		return nil, err
	}
	return imp, nil
}
