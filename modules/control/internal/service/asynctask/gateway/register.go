package gateway

import (
	"github.com/mooyang-code/moox/modules/control/internal/gateway"
	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterAsyncTaskGateway 注册异步任务网关
func RegisterAsyncTaskGateway(asyncTaskService asynctask.Service) {
	log.Info("[AsyncTask Gateway] 正在注册异步任务网关...")
	handler := NewAsyncTaskGatewayHandler(asyncTaskService)

	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	log.Infof("[AsyncTask Gateway] 已注册异步任务网关处理器: %s", handler.ServiceID())
}
