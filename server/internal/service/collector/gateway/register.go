package gateway

import (
	"github.com/mooyang-code/moox/server/internal/gateway"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	collectormgr "github.com/mooyang-code/moox/server/internal/service/collector/manager"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterGatewayHandler 注册网关处理器到全局网关系统
func RegisterGatewayHandler(handler *GatewayHandler) {
	// 获取网关实例并注册处理器
	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	log.Infof("已注册采集器网关处理器: %s", handler.ServiceID())
}

// RegisterCollectorGateway 注册采集器网关（便捷函数）
// 封装创建GatewayHandler和注册的过程
func RegisterCollectorGateway(collectorFactory *collectormgr.ServiceFactory, getCloudProvider func(string) provider.Client) {
	log.Info("[Collector Gateway] 正在注册采集器网关...")
	handler := NewGatewayHandler(collectorFactory, getCloudProvider)
	RegisterGatewayHandler(handler)
	log.Info("[Collector Gateway] 采集器网关注册完成")
}
