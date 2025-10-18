package gateway

import (
	"github.com/mooyang-code/moox/server/internal/gateway"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterGatewayHandler 注册网关处理器到全局网关系统
func RegisterGatewayHandler(handler *GatewayHandler) {
	// 获取网关实例并注册处理器
	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	log.Infof("已注册采集器网关处理器: %s", handler.ServiceID())
}
