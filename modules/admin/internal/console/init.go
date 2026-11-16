package console

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitConsoleServices 在两个分派器都就绪后注册浏览器路由；sessions 是与认证服务共用的会话表。
func InitConsoleServices(s *server.Server, local *LocalDispatcher, gateway RPCForwarder, sessions *session.Store, authorizers ...SpaceAuthorizer) error {
	if GetConfig() == nil {
		return fmt.Errorf("控制台配置未初始化")
	}
	if local == nil || gateway == nil || sessions == nil {
		return fmt.Errorf("控制台需要本地分派器、网关客户端及会话表")
	}
	return RegisterConsoleHTTPHandlers(s, local, gateway, sessions, authorizers...)
}
