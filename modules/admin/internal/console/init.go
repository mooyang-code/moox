package console

import (
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitConsoleServices 初始化控制台路由。
// Admin 仅承载浏览器控制面和 Gateway 路由快照接口；机器调用由独立 Gateway 承载。
func InitConsoleServices(s *server.Server, provider ConsoleProvider, adminNodeID string, authorizers ...TradeSpaceAuthorizer) error {
	cfg := GetConfig()
	if cfg == nil {
		return fmt.Errorf("控制台配置未初始化")
	}
	if strings.TrimSpace(adminNodeID) == "" {
		return fmt.Errorf("admin node id is required")
	}
	log.Info("控制台转发服务地址将从 t_service_deployments active 记录解析")

	log.Info("正在注册控制台 HTTP 路由...")
	if err := RegisterConsoleHTTPHandlers(s, provider, adminNodeID, authorizers...); err != nil {
		return fmt.Errorf("注册控制台 HTTP 路由失败: %w", err)
	}
	log.Info("控制台 HTTP 路由注册完成")
	return nil
}
