package bootstrap

import (
	"context"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode"
	collectormgr "github.com/mooyang-code/moox/server/internal/service/collector/manager"
	"github.com/mooyang-code/moox/server/internal/service/database"
	"github.com/mooyang-code/moox/server/internal/service/fileserver"
	sshapp "github.com/mooyang-code/moox/server/internal/service/ssh/app"

	"trpc.group/trpc-go/trpc-go/log"
)

// Services 应用服务集合
type Services struct {
	// 数据库管理器（共享基础模块）
	DBManager *database.Manager

	// 各模块服务
	AsyncTaskService asynctask.Service
	CloudNodeService cloudnode.Service
	HeartbeatService cloudnode.HeartbeatService

	// Collector服务工厂（仅用于collector自己的API）
	CollectorFactory *collectormgr.ServiceFactory
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

// createCoreServices 创建核心服务（只创建实例，不启动）
func createCoreServices(dbManager *database.Manager, cfg *Config) (*Services, error) {
	log.Info("[Bootstrap] 正在创建核心服务...")

	// 创建异步任务管理服务
	log.Info("[Bootstrap] 正在创建异步任务管理服务...")
	asyncTaskService := asynctask.NewService(dbManager)

	// 创建云节点服务（已集成心跳服务）
	log.Info("[Bootstrap] 正在创建云节点服务...")
	cloudNodeService := cloudnode.NewService(dbManager, asyncTaskService)

	// 心跳服务已集成到云节点服务中，直接使用相同的实例
	heartbeatService := cloudNodeService.(cloudnode.HeartbeatService)

	// 创建Collector服务工厂（仅用于collector自己的API路由）
	collectorFactory := collectormgr.NewServiceFactory(dbManager)

	log.Info("[Bootstrap] 核心服务创建完成")

	return &Services{
		DBManager:        dbManager,
		AsyncTaskService: asyncTaskService,
		CloudNodeService: cloudNodeService,
		HeartbeatService: heartbeatService,
		CollectorFactory: collectorFactory,
	}, nil
}

// registerAsyncExecutors 注册所有模块的异步任务处理器
func registerAsyncExecutors(services *Services) error {
	log.Info("[Bootstrap] 正在注册异步任务处理器...")

	// cloudnode模块自注册所有异步任务执行器（节点管理 + 代码包管理）
	err := cloudnode.RegisterExecutors(
		services.DBManager,
		services.CloudNodeService,
		services.HeartbeatService,
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

	// 1. 启动心跳服务（包含prober）
	log.Info("[Bootstrap] 正在启动心跳服务...")
	if err := services.HeartbeatService.StartHeartbeatService(ctx); err != nil {
		log.Errorf("[Bootstrap] 启动心跳服务失败: %v", err)
		return err
	}
	log.Info("[Bootstrap] 心跳服务已启动（包含探测器）")

	// 2. 启动异步任务 Worker
	log.Info("[Bootstrap] 正在启动异步任务 Worker...")
	if err := services.AsyncTaskService.StartWorker(ctx, workerCount); err != nil {
		log.Errorf("[Bootstrap] 启动异步任务 Worker失败: %v", err)
		return err
	}
	log.Infof("[Bootstrap] 异步任务 Worker已启动 (count=%d)", workerCount)

	// 3. 启动文件下载服务（在独立的goroutine中运行）
	log.Info("[Bootstrap] 正在启动文件下载服务...")
	fileserver.StartFileDownloadService()

	// 4. 启动WebSSH服务（在独立的goroutine中运行）
	log.Info("[Bootstrap] 正在启动WebSSH服务...")
	sshapp.StartWebSSHService()

	log.Info("[Bootstrap] 所有后台服务已启动")
	return nil
}
