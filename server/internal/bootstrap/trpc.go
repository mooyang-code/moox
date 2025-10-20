package bootstrap

import (
	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/server/internal/gateway"
	asynctaskgateway "github.com/mooyang-code/moox/server/internal/service/asynctask/gateway"
	authsvr "github.com/mooyang-code/moox/server/internal/service/auth"
	cloudnodegateway "github.com/mooyang-code/moox/server/internal/service/cloudnode/gateway"
	collectorgateway "github.com/mooyang-code/moox/server/internal/service/collector/gateway"
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy"
	pb "github.com/mooyang-code/moox/server/proto/gen"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// RegisterTRPCServices 注册所有TRPC服务
// 包括：认证服务、心跳服务、网关服务、采集器网关、定时器
func RegisterTRPCServices(s *server.Server, cfg *Config, services *Services) error {
	log.Info("正在注册TRPC服务...")

	// 1. 注册认证服务
	authImp, err := authsvr.NewService(cfg.Auth, services.DBManager)
	if err != nil {
		return err
	}
	pb.RegisterAuthAPIService(s, authImp)
	log.Info("认证服务注册完成")

	// 2. 注册心跳服务（已迁移到HTTP API，无需在此注册）
	// heartbeatImp, err := heartbeatapi.NewHeartbeatService(cfg.Heartbeat, services.DBManager)
	// if err != nil {
	// 	return err
	// }
	// pb.RegisterCloudNodeAPIService(s, heartbeatImp)
	log.Info("心跳服务使用HTTP API，无需tRPC注册")

	// 启动心跳管理器的后台任务（在services.go中已处理）
	// ctx := trpc.BackgroundContext()
	// heartbeatImp.Start(ctx)
	log.Info("心跳管理器后台任务已启动")

	// 3. 初始化网关服务
	log.Info("正在初始化网关服务...")
	gateway.InitGatewayServices(s)
	log.Info("网关服务初始化完成")

	// 4. 注册各模块网关（必须在网关服务初始化之后）
	// 4.1 注册异步任务网关
	asynctaskgateway.RegisterAsyncTaskGateway(services.AsyncTaskService)

	// 4.2 注册云节点网关（包含云账户和代码包管理）
	cloudnodegateway.RegisterCloudNodeGateway(
		services.CloudNodeService,
		services.AsyncTaskService,
	)

	// 4.4 注册采集器网关（只包含采集器自己的功能）
	collectorgateway.RegisterCollectorGateway(services.CollectorFactory, services.CloudNodeService.GetProviderByAccount)

	// 5. 注册定时器
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsproxy.timer"), dnsproxy.DnsproxySchedule)
	log.Info("定时器注册完成")

	log.Info("TRPC服务注册完成")
	return nil
}
