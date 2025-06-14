package gateway

import (
	"github.com/mooyang-code/moox/server/internal/config"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitGatewayServices 初始化网关服务处理器和路由
func InitGatewayServices(s *server.Server) {
	// 加载配置文件
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("加载网关配置失败: %v", err)
	}

	gateway := GetGatewayHandleInstance()

	// 动态注册配置文件中的所有服务
	serviceIDs := cfg.GetAllServiceIDs()
	for _, serviceID := range serviceIDs {
		serviceConfig, err := cfg.GetServiceConfigByID(serviceID)
		if err != nil {
			log.Fatalf("获取服务配置失败: %v", err)
		}
		handler := NewHTTPServiceHandler(serviceID, serviceConfig)
		gateway.Register(handler)
		log.Infof("已注册服务处理器: %s -> %s/%s", serviceID, serviceConfig.BaseURL, serviceConfig.ServicePath)
	}

	log.Infof("网关服务处理器初始化完成，已注册服务: %v", gateway.GetRegisteredServices())

	// 注册网关HTTP路由
	log.Info("正在注册网关HTTP路由...")
	RegisterGatewayHTTPHandlers(s)
	log.Info("网关HTTP路由注册完成")
}
