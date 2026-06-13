package gateway

import (
	"github.com/mooyang-code/moox/modules/control/internal/gateway"
	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"
	cloudnodemgr "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterCloudNodeGateway 注册云节点网关（包含云账户和代码包管理功能）
func RegisterCloudNodeGateway(service cloudnodemgr.Service, asyncTaskService asynctask.Service) {
	log.Info("[CloudNode Gateway] 正在注册云节点网关...")
	handler := NewCloudNodeGatewayHandler(service, asyncTaskService)

	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	log.Infof("[CloudNode Gateway] 已注册云节点网关处理器: %s", handler.ServiceID())
}
