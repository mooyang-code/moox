package gateway

import (
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitGatewayServices 初始化网关路由。
// 网关已退化为纯 HTTP 反向代理：serviceID→address/path 由 gateway.yaml 配置，
// 运行时由 forwardHTTP 透传，无需再注册 ServiceHandler/HTTPServiceHandler。
func InitGatewayServices(s *server.Server) {
	cfg := GetConfig()
	if cfg == nil {
		log.Fatalf("网关配置未初始化")
	}
	log.Infof("网关已配置透传服务: %v", cfg.GetAllServiceIDs())

	// 注册网关HTTP路由
	log.Info("正在注册网关HTTP路由...")
	RegisterGatewayHTTPHandlers(s)
	log.Info("网关HTTP路由注册完成")
}
