package gateway

import (
	"github.com/mooyang-code/moox/server/internal/gateway"
	"github.com/mooyang-code/moox/server/internal/service/monitor"
	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterMonitorGateway 注册监控服务网关
func RegisterMonitorGateway(svc monitor.Service) {
	log.Info("[Monitor Gateway] 正在注册 Monitor 网关...")
	handler := NewMonitorGatewayHandler(svc)

	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	log.Infof("[Monitor Gateway] 已注册 Monitor 网关处理器: %s", handler.ServiceID())
}
