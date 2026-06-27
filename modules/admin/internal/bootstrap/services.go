package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"
	"github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode"
	cloudnodedao "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr"
	collectordao "github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/dao"
	collectmgr_planner "github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/planner"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"
	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor"
	"github.com/mooyang-code/moox/modules/admin/internal/service/space"
	ssh "github.com/mooyang-code/moox/modules/admin/internal/service/ssh"
	sshdao "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/dao"

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
	SpaceMgr     space.Service
	AsyncTask asynctask.Service
	CloudNodeMgr cloudnode.Service

	// Collector服务实例
	TaskRuleService       collectmgr.TaskRuleService
	TaskInstanceService   collectmgr.TaskInstanceService
	DataTypeConfigService collectmgr.DataTypeConfigService
	TaskPlannerService    collectmgr.TaskPlannerService

	// SSH 服务
	SSHService ssh.Service

	// 监控服务
	Monitor monitor.Service
}

// StartBackgroundServices 启动所有后台服务
// 包括：AsyncTask服务、采集器服务、文件下载服务、WebSSH服务等
func StartBackgroundServices(ctx context.Context, cfg *Config) (*Services, error) {
	log.Info("正在启动后台服务...")

	// 1. 初始化数据库
	dbManager, err := initializeDatabase(&cfg.App.Database)
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
func initializeDatabase(dbCfg *config.DatabaseConfig) (*database.Manager, error) {
	log.Info("[Bootstrap] 正在初始化数据库...")

	dbManager := database.NewManager()
	if err := dbManager.Initialize(dbCfg); err != nil {
		log.Errorf("[Bootstrap] 初始化数据库失败: %v", err)
		return nil, err
	}

	log.Info("[Bootstrap] 数据库初始化成功")
	return dbManager, nil
}

// createCoreServices 创建核心服务
func createCoreServices(dbManager *database.Manager, cfg *Config) (*Services, error) {
	log.Info("[Bootstrap] 正在创建核心服务...")

	// 创建 Space 服务
	log.Info("[Bootstrap] 正在创建 Space 服务...")
	spaceService := space.NewService(dbManager)

	// 创建异步任务管理服务
	log.Info("[Bootstrap] 正在创建异步任务管理服务...")
	asyncTask := asynctask.NewService(dbManager)

	// 创建云节点服务（已集成心跳服务）
	// 注意：这里先创建服务，稍后注入任务实例仓库
	log.Info("[Bootstrap] 正在创建云节点服务...")
	cloudNode := cloudnode.NewService(dbManager, asyncTask, cfg.CloudNode)
	if err := cloudnode.InitKeepaliveInstance(cloudNode); err != nil {
		return nil, err
	}

	// 创建Collector服务实例
	// 创建所需的DAO
	db := dbManager.GetDB()
	taskRulesDAO := collectordao.NewCollectorTaskRulesDAO(db)
	instanceDAO := collectordao.NewCollectorTaskInstanceDAO(db)
	dataTypeConfigDAO := collectordao.NewCollectorDataTypeConfigsDAO(db)
	fieldConfigDAO := collectordao.NewCollectorFieldConfigsDAO(db)
	nodeDAO := cloudnodedao.NewCloudNodeDAO(db)

	// 创建内存任务实例仓库
	log.Info("[Bootstrap] 正在创建内存任务实例仓库...")
	memStore := collectmgr.NewTaskInstanceStore()
	// 注释：不加载历史数据，等待首次定时重算填充

	// 创建任务规划器实例（注入内存仓库）
	log.Info("[Bootstrap] 正在创建任务规划器...")
	// cloudNode 实现了 OnlineNodeIDsProvider 接口，用于获取在线节点ID列表
	registry := collectmgr_planner.NewPlannerRegistry(nodeDAO, nil, cloudNode)
	taskPlanner := collectmgr.NewTaskPlannerServiceImpl(taskRulesDAO, instanceDAO, registry, nodeDAO, cloudNode, memStore)

	// 创建服务实例
	taskRuleService := collectmgr.NewTaskRulesServiceImpl(taskRulesDAO, nodeDAO)
	taskInstanceService := collectmgr.NewTaskInstanceServiceImpl(instanceDAO, taskRulesDAO, nodeDAO)
	dataTypeConfigService := collectmgr.NewDataTypeConfigServiceImpl(dataTypeConfigDAO, fieldConfigDAO, db)

	// 注入 CloudNodeMgr 依赖到 TaskInstanceService（解决循环依赖）
	// 创建适配器以匹配接口签名
	if impl, ok := taskInstanceService.(*collectmgr.TaskInstanceServiceImpl); ok {
		invoker := &cloudFunctionInvokerAdapter{service: cloudNode}
		impl.SetCloudNodeService(invoker)
		// 注入内存仓库，用于状态同步（ReportTaskStatus时同步更新内存）
		impl.SetTaskInstanceStore(memStore)
	}

	// 【新增】注入任务实例仓库到 CloudNodeMgr（用于心跳任务下发）
	taskStoreAdapter := collectmgr.NewTaskStoreAdapter(memStore)
	if serviceImpl, ok := cloudNode.(*cloudnode.ServiceImpl); ok {
		serviceImpl.SetTaskInstanceStore(taskStoreAdapter)
	}

	// 初始化DNSProxy实例（全局单例，供定时器使用）
	log.Info("[Bootstrap] 正在初始化DNSProxy实例...")
	dnsproxy.InitDNSProxyInstance()

	// 创建 SSH 服务
	log.Info("[Bootstrap] 正在创建 SSH 服务...")
	sshHostDAO := sshdao.NewSSHHostDAO(db)
	sshSessionDAO := sshdao.NewSSHSessionDAO(db)
	sshService := ssh.NewService(sshHostDAO, sshSessionDAO)

	// 创建监控服务
	log.Info("[Bootstrap] 正在创建监控服务...")
	monitorService := monitor.NewService(dbManager)
	monitor.InitMonitorInstance(dbManager)

	log.Info("[Bootstrap] 核心服务创建完成")
	services := &Services{
		DBManager:             dbManager,
		SpaceMgr:          spaceService,
		AsyncTask:      asyncTask,
		CloudNodeMgr:      cloudNode,
		TaskRuleService:       taskRuleService,
		TaskInstanceService:   taskInstanceService,
		DataTypeConfigService: dataTypeConfigService,
		TaskPlannerService:    taskPlanner,
		SSHService:            sshService,
		Monitor:        monitorService,
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
		services.CloudNodeMgr,
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
	if err := services.AsyncTask.StartWorker(ctx, workerCount); err != nil {
		log.Errorf("[Bootstrap] 启动异步任务 Worker失败: %v", err)
		return err
	}
	log.Infof("[Bootstrap] 异步任务 Worker已启动 (count=%d)", workerCount)

	// 2. CloudNode服务已通过网关方式启动，无需独立HTTP服务
	// 3. 文件下载服务已并入统一网关 rawhandler（/api/admin/fileserver/download），无需独立端口
	// 4. SSH 直连端点（WebSocket/SFTP 上传下载）已并入统一网关 rawhandler（/api/admin/ssh/*），废弃独立端口 20180

	log.Info("[Bootstrap] 所有后台服务已启动")
	return nil
}

// startSSHDirectServer 已废弃：SSH 直连端点（WebSocket/SFTP 上传下载）已并入统一网关 rawhandler，
// 不再需要独立 HTTP 服务（端口 20180）。

