package gateway

import (
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitGatewayServices 初始化网关路由。
// Admin 仅承载浏览器控制面和 Gateway 路由快照接口；机器调用由独立 Gateway 承载。
func InitGatewayServices(s *server.Server, provider GatewayProvider, adminNodeID string) error {
	cfg := GetConfig()
	if cfg == nil {
		return fmt.Errorf("网关配置未初始化")
	}
	if strings.TrimSpace(adminNodeID) == "" {
		return fmt.Errorf("admin node id is required")
	}
	log.Info("网关透传服务地址将从 t_service_deployments active 记录解析")

	// 注册网关HTTP路由
	log.Info("正在注册网关HTTP路由...")
	if err := RegisterGatewayHTTPHandlers(s, provider, adminNodeID); err != nil {
		return fmt.Errorf("注册网关 HTTP 路由失败: %w", err)
	}
	log.Info("网关HTTP路由注册完成")
	return nil
}
