package bootstrap

import (
	"github.com/mooyang-code/moox/server/internal/gateway"
	asynctaskgateway "github.com/mooyang-code/moox/server/internal/service/asynctask/gateway"
	authsvr "github.com/mooyang-code/moox/server/internal/service/auth"
	cloudnodegateway "github.com/mooyang-code/moox/server/internal/service/cloudnode/gateway"
	collectorgateway "github.com/mooyang-code/moox/server/internal/service/collectmgr/gateway"
	pb "github.com/mooyang-code/moox/server/proto/gen"

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
	)
	log.Info("TRPC 服务注册完成")
	return nil
}
