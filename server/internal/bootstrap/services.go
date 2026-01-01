package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode"
	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	collectmgr_planner "github.com/mooyang-code/moox/server/internal/service/collectmgr/planner"
	"github.com/mooyang-code/moox/server/internal/service/database"
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy"
	"github.com/mooyang-code/moox/server/internal/service/fileserver"
	sshapp "github.com/mooyang-code/moox/server/internal/service/ssh/app"

	"trpc.group/trpc-go/trpc-go/log"
)

// cloudFunctionInvokerAdapter 适配器，将 cloudnode.Service 适配为 collectmgr.CloudFunctionInvoker
// 用于解决接口返回类型不匹配的问题
type cloudFunctionInvokerAdapter struct {
	service cloudnode.Service
}

// InvokeFunction 实现 collectmgr.CloudFunctionInvoker 接口
func (a *cloudFunctionInvokerAdapter) InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (interface{}, error) {
	return a.service.InvokeFunction(ctx, nodeID, eventData)
}

// Services 应用服务集合
type Services struct {
	// 数据库管理器（共享基础模块）
	DBManager *database.Manager

	// 各模块服务
	AsyncTaskService asynctask.Service
	CloudNodeService cloudnode.Service

	// Collector服务实例
	TaskRuleService       collectmgr.TaskRuleService
	TaskInstanceService   collectmgr.TaskInstanceService
	DataTypeConfigService collectmgr.DataTypeConfigService
	TaskPlannerService    collectmgr.TaskPlannerService
}

// StartBackgroundServices 启动所有后台服务
// 包括：AsyncTask服务、采集器服务、文件下载服务、WebSSH服务等
func StartBackgroundServices(ctx context.Context, cfg *Config) (*Services, error) {
	log.Info("正在启动后台服务...")

	// 1. 初始化数据库
	dbManager, err := initializeDatabase(cfg.App.Database.Path)
	if err != nil {
		return nil, err
	}

	// 2. 创建核心服务（只创建，不启动）
	services, err := createCoreServices(dbManager, cfg)
	if err != nil {
		return nil, err
	}

	// 3. 注册异步任务处理器
	err = registerAsyncExecutors(services)
	if err != nil {
		return nil, err
	}

	// 4. 启动所有后台服务
	if err := startBackgroundWorkers(ctx, services, cfg.App.Worker.AsyncTaskWorkerCount); err != nil {
		return nil, err
	}

	log.Info("后台服务启动完成")
	return services, nil
}

// initializeDatabase 初始化数据库
func initializeDatabase(dbPath string) (*database.Manager, error) {
	log.Info("[Bootstrap] 正在初始化数据库...")

	dbManager := database.NewManager()
	if err := dbManager.Initialize(dbPath); err != nil {
		log.Errorf("[Bootstrap] 初始化数据库失败: %v", err)
		return nil, err
	}

	log.Info("[Bootstrap] 数据库初始化成功")
	return dbManager, nil
}

// createCoreServices 创建核心服务
func createCoreServices(dbManager *database.Manager, cfg *Config) (*Services, error) {
	log.Info("[Bootstrap] 正在创建核心服务...")

	// 创建异步任务管理服务
	log.Info("[Bootstrap] 正在创建异步任务管理服务...")
	asyncTaskService := asynctask.NewService(dbManager)

	// 创建云节点服务（已集成心跳服务）
	log.Info("[Bootstrap] 正在创建云节点服务...")
	cloudNodeService := cloudnode.NewService(dbManager, asyncTaskService, cfg.CloudNode)

	// 创建Collector服务实例
	// 创建所需的DAO
	taskRulesDAO := collectordao.NewCollectorTaskRulesDAO(dbManager.GetDB())
	instanceDAO := collectordao.NewCollectorTaskInstanceDAO(dbManager.GetDB())
	dataTypeConfigDAO := collectordao.NewCollectorDataTypeConfigsDAO(dbManager.GetDB())
	fieldConfigDAO := collectordao.NewCollectorFieldConfigsDAO(dbManager.GetDB())
	nodeDAO := cloudnodedao.NewCloudNodeDAO(dbManager.GetDB())
	heartbeatDAO := cloudnodedao.NewHeartbeatNodeDAO(dbManager.GetDB())

	// 创建任务规划器实例（不再需要全局单例，因为改为客户端轮询）
	log.Info("[Bootstrap] 正在创建任务规划器...")
	registry := collectmgr_planner.NewPlannerRegistry(nodeDAO, nil)
	taskPlanner := collectmgr.NewTaskPlannerServiceImpl(taskRulesDAO, instanceDAO, registry, nodeDAO)

	// 初始化心跳探测器（全局单例，供定时器使用）注意：必须在 NewService 之后调用，因为 NewService 会注册全局探测器
	log.Info("[Bootstrap] 正在初始化心跳探测器...")
	cloudnode.InitProberInstance(dbManager, cfg.CloudNode)

	// 创建服务实例
	taskRuleService := collectmgr.NewTaskRulesServiceImpl(taskRulesDAO, nodeDAO)
	taskInstanceService := collectmgr.NewTaskInstanceServiceImpl(instanceDAO, taskRulesDAO, nodeDAO, heartbeatDAO)
	dataTypeConfigService := collectmgr.NewDataTypeConfigServiceImpl(dataTypeConfigDAO, fieldConfigDAO, dbManager.GetDB())

	// 注入 CloudNodeService 依赖到 TaskInstanceService（解决循环依赖）
	// 创建适配器以匹配接口签名
	if impl, ok := taskInstanceService.(*collectmgr.TaskInstanceServiceImpl); ok {
		invoker := &cloudFunctionInvokerAdapter{service: cloudNodeService}
		impl.SetCloudNodeService(invoker)
	}

	// 初始化DNSProxy实例（全局单例，供定时器使用）
	log.Info("[Bootstrap] 正在初始化DNSProxy实例...")
	dnsproxy.InitDNSProxyInstance()

	log.Info("[Bootstrap] 核心服务创建完成")
	services := &Services{
		DBManager:             dbManager,
		AsyncTaskService:      asyncTaskService,
		CloudNodeService:      cloudNodeService,
		TaskRuleService:       taskRuleService,
		TaskInstanceService:   taskInstanceService,
		DataTypeConfigService: dataTypeConfigService,
		TaskPlannerService:    taskPlanner,
	}

	// 初始化 TaskPlanner 全局实例（供定时器使用）
	log.Info("[Bootstrap] 正在初始化 TaskPlanner 全局实例...")
	collectmgr.InitTaskPlannerInstance(taskPlanner)

	return services, nil
}

// registerAsyncExecutors 注册所有模块的异步任务处理器
func registerAsyncExecutors(services *Services) error {
	log.Info("[Bootstrap] 正在注册异步任务处理器...")

	// cloudnode模块自注册所有异步任务执行器（节点管理 + 代码包管理）
	err := cloudnode.RegisterExecutors(
		services.DBManager,
		services.CloudNodeService,
		services.CloudNodeService,
	)
	if err != nil {
		return err
	}

	log.Info("[Bootstrap] 异步任务处理器注册完成")
	return nil
}

// startBackgroundWorkers 启动所有后台服务
func startBackgroundWorkers(ctx context.Context, services *Services, workerCount int) error {
	log.Info("[Bootstrap] 正在启动后台服务...")

	// 1. 启动异步任务 Worker
	log.Info("[Bootstrap] 正在启动异步任务 Worker...")
	if err := services.AsyncTaskService.StartWorker(ctx, workerCount); err != nil {
		log.Errorf("[Bootstrap] 启动异步任务 Worker失败: %v", err)
		return err
	}
	log.Infof("[Bootstrap] 异步任务 Worker已启动 (count=%d)", workerCount)

	// 2. 启动文件下载服务（在独立的goroutine中运行）
	log.Info("[Bootstrap] 正在启动文件下载服务...")
	fileserver.StartFileDownloadService()

	// 3. 启动WebSSH服务（在独立的goroutine中运行）
	log.Info("[Bootstrap] 正在启动WebSSH服务...")
	sshapp.StartWebSSHService()

	// 4. CloudNode服务已通过网关方式启动，无需独立HTTP服务

	log.Info("[Bootstrap] 所有后台服务已启动")
	return nil
}
