package gateway

import (
	"github.com/mooyang-code/moox/server/internal/gateway"
	"github.com/mooyang-code/moox/server/internal/service/collector"

	"trpc.group/trpc-go/trpc-go/log"
)

// RegisterCollectorGateway 注册采集器网关
func RegisterCollectorGateway(taskRuleService collector.TaskRuleService, taskInstanceService collector.TaskInstanceService, dataTypeConfigService collector.DataTypeConfigService) {
	log.Info("[Collector Gateway] 正在注册采集器网关...")
	
	// 创建网关处理器
	handler := NewGatewayHandler(taskRuleService, taskInstanceService, dataTypeConfigService)
	
	// 注册到全局网关系统
	gw := gateway.GetGatewayHandleInstance()
	gw.Register(handler)
	
	log.Infof("已注册采集器网关处理器: %s", handler.ServiceID())
	log.Info("[Collector Gateway] 采集器网关注册完成")
}
