package console

import (
	"fmt"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitConsoleServices registers browser routes after both dispatchers are ready.
func InitConsoleServices(s *server.Server, local *LocalDispatcher, gateway RPCForwarder, authorizers ...TradeSpaceAuthorizer) error {
	if GetConfig() == nil {
		return fmt.Errorf("控制台配置未初始化")
	}
	if local == nil || gateway == nil {
		return fmt.Errorf("控制台需要本地分派器及网关客户端")
	}
	return RegisterConsoleHTTPHandlers(s, local, gateway, authorizers...)
}
