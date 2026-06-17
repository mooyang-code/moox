package gateway

import (
	"github.com/mooyang-code/moox/modules/control/internal/gateway"
	ssh "github.com/mooyang-code/moox/modules/control/internal/service/ssh"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterSSHGateway 注册 SSH 网关
func RegisterSSHGateway(svc ssh.Service) {
	log.Info("[SSH Gateway] 正在注册 SSH 网关...")
	handler := NewSSHGatewayHandler(svc)

	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	log.Infof("[SSH Gateway] 已注册 SSH 网关处理器: %s", handler.ServiceID())
}
