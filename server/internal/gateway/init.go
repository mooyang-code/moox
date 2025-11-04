package gateway

import (
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// InitGatewayServices 初始化网关服务处理器和路由
func InitGatewayServices(s *server.Server) {
	// 从依赖注入获取配置
	cfg := GetConfig()
	if cfg == nil {
		log.Fatalf("网关配置未初始化")
	}

	gateway := GetGatewayHandleInstance()

	// 动态注册配置文件中的所有服务
	serviceIDs := cfg.GetAllServiceIDs()
	for _, serviceID := range serviceIDs {
		// 跳过collector和cloudnode服务，它们将使用自定义的处理器
		if serviceID == "collector" || serviceID == "cloudnode" {
			log.Infof("跳过%s服务的HTTPServiceHandler注册，将使用自定义处理器", serviceID)
			continue
		}

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
