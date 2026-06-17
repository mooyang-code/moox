package bootstrap

import (
	"github.com/mooyang-code/moox/modules/control/internal/gateway"
	asynctaskgateway "github.com/mooyang-code/moox/modules/control/internal/service/asynctask/gateway"
	authsvr "github.com/mooyang-code/moox/modules/control/internal/service/auth"
	cloudnodegateway "github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/gateway"
	collectorgateway "github.com/mooyang-code/moox/modules/control/internal/service/collectmgr/gateway"
	controlsvc "github.com/mooyang-code/moox/modules/control/internal/service/control"
	dnsproxygateway "github.com/mooyang-code/moox/modules/control/internal/service/dnsproxy/gateway"
	monitorgateway "github.com/mooyang-code/moox/modules/control/internal/service/monitor/gateway"
	sshgateway "github.com/mooyang-code/moox/modules/control/internal/service/ssh/gateway"
	controlpb "github.com/mooyang-code/moox/modules/control/proto/controlgen"
	pb "github.com/mooyang-code/moox/modules/control/proto/gen"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// RegisterTRPCServices 注册所有TRPC服务
// 包括：认证服务、心跳服务、网关服务、采集器网关、定时器
func RegisterTRPCServices(s *server.Server, cfg *Config, services *Services) error {
	// 1. 注册认证服务
	log.Info("正在初始化认证服务...")
	authImp, err := authsvr.NewService(cfg.Auth, services.DBManager)
	if err != nil {
		return err
	}
	pb.RegisterAuthAPIService(s, authImp)

	// 注册新量化数据系统控制面协议。该服务只暴露编排语义，不暴露 storage adapter。
	controlImp := controlsvc.NewService()
	controlpb.RegisterControlServiceService(s, controlImp)
	controlpb.RegisterCollectorServiceService(s, controlImp)
	controlpb.RegisterNodeServiceService(s, controlImp)
	controlpb.RegisterTaskServiceService(s, controlImp)

	// 2. 初始化网关服务
	log.Info("正在初始化网关服务...")
	gateway.InitGatewayServices(s)

	// 3. 注册各模块网关（必须在网关服务初始化之后）
	// 3.1 注册异步任务网关
	asynctaskgateway.RegisterAsyncTaskGateway(services.AsyncTaskService)

	// 3.2 注册云节点网关（包含云账户和代码包管理）
	cloudnodegateway.RegisterCloudNodeGateway(
		services.CloudNodeService,
		services.AsyncTaskService,
	)

	// 3.3 注册采集器网关（只包含采集器任务管理的功能）
	collectorgateway.RegisterCollectorGateway(
		services.TaskRuleService,
		services.TaskInstanceService,
		services.DataTypeConfigService,
		services.TaskPlannerService,
	)

	// 3.4 注册DNS代理网关
	dnsproxygateway.RegisterDNSProxyGateway()

	// 3.5 注册 SSH 网关
	sshgateway.RegisterSSHGateway(services.SSHService)

	// 3.6 注册监控服务网关
	monitorgateway.RegisterMonitorGateway(services.MonitorService)

	log.Info("TRPC 服务注册完成")
	return nil
}
