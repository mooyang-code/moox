package gateway

import (
	"github.com/mooyang-code/moox/modules/control/internal/gateway"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterDNSProxyGateway 注册DNS代理网关
func RegisterDNSProxyGateway() {
	log.Info("[DNSProxy Gateway] 正在注册DNS代理网关...")

	// 创建网关处理器
	handler := NewGatewayHandler()

	// 注册到全局网关系统
	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)

	log.Infof("已注册DNS代理网关处理器: %s", handler.ServiceID())
	log.Info("[DNSProxy Gateway] DNS代理网关注册完成")
}
