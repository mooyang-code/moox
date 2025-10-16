package collector

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	cloudnodequeue "github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	nodeheartbeat "github.com/mooyang-code/moox/server/internal/service/nodeservice/heartbeat"
	packagemgrlogic "github.com/mooyang-code/moox/server/internal/service/packagemgr/logic"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// CollectorServiceImpl 采集器服务实现
type CollectorServiceImpl struct {
	db               *gorm.DB
	serviceManager   *ServiceManager
	httpMux          *http.ServeMux
	cloudProvider    provider.CloudProvider
	queueManager     *cloudnodequeue.QueueManager
	heartbeatManager *nodeheartbeat.Manager
	asyncTaskService asynctasklogic.AsyncTaskService
}

// InitCollectorServiceImpl 初始化采集器服务实现
func InitCollectorServiceImpl(dbPath string) (*CollectorServiceImpl, error) {
	impl := &CollectorServiceImpl{}

	// 初始化数据库连接
	db, err := impl.initDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	impl.db = db

	// 初始化队列管理器
	impl.queueManager = cloudnodequeue.NewQueueManager(db)

	// 初始化心跳管理器
	// TODO: 需要配置实际的moox服务URL
	impl.heartbeatManager = nodeheartbeat.NewManager(db, "http://localhost:8080")

	// 初始化异步任务服务
	impl.asyncTaskService = asynctasklogic.NewAsyncTaskService(db)

	// 初始化服务管理器
	impl.serviceManager = NewServiceManager(db, impl.queueManager)

	return impl, nil
}

// initDB 初始化数据库连接
func (s *CollectorServiceImpl) initDB(dbPath string) (*gorm.DB, error) {
	if dbPath == "" {
		dbPath = "./data/moox.db"
	}

	// 确保目录存在
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	log.Infof("初始化SQLite数据库连接: %s", dbPath)
	return db, nil
}

// GetDB 获取数据库连接
func (s *CollectorServiceImpl) GetDB() *gorm.DB {
	return s.db
}

// RegisterCollectorHandlers 注册采集器处理器（注册执行器）
func (s *CollectorServiceImpl) RegisterCollectorHandlers() {
	// 注册异步任务执行器（BATCH_CREATE_NODE, BATCH_DELETE_NODE, BATCH_DEPLOY_NODE）
	// 使用共享的异步任务服务实例
	asyncTaskService := s.asyncTaskService

	// 注册任务执行器
	// 创建云节点服务（带队列管理器）
	scfNodeService := cloudnodelogic.NewSCFNodeServiceWithQueue(s.db, s.queueManager)

	// 创建批量创建节点执行器
	batchCreateNodeExecutor := logic.NewBatchCreateNodeExecutor(s.db, scfNodeService, asyncTaskService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(batchCreateNodeExecutor.GetTaskType(), batchCreateNodeExecutor)

	// 创建批量删除节点执行器
	batchDeleteNodeExecutor := logic.NewBatchDeleteNodeExecutor(s.db, scfNodeService, asyncTaskService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(batchDeleteNodeExecutor.GetTaskType(), batchDeleteNodeExecutor)

	// 创建包管理服务（用于批量部署）
	var packageService *packagemgrlogic.FunctionPackageService
	if s.cloudProvider != nil {
		if cosProvider, ok := s.cloudProvider.(provider.CloudProviderWithCOS); ok {
			cosBucket := "moox-packages" // 从配置中获取
			packageService = packagemgrlogic.NewFunctionPackageService(s.db, cosProvider, cosBucket)
		} else {
			log.Warn("[Collector Service] CloudProvider 不支持COS功能，跳过包管理服务初始化")
		}
	} else {
		log.Warn("[Collector Service] CloudProvider 未设置，跳过包管理服务初始化")
	}

	// 创建批量部署节点执行器
	batchDeployNodeExecutor := logic.NewBatchDeployNodeExecutor(s.db, scfNodeService, asyncTaskService, packageService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(batchDeployNodeExecutor.GetTaskType(), batchDeployNodeExecutor)

	log.Info("[Collector Service] 采集器处理器注册完成，已注册任务执行器：BATCH_CREATE_NODE, BATCH_DELETE_NODE, BATCH_DEPLOY_NODE")
}

// RegisterCollectorGateway 注册采集器网关（注册到网关接口）
func (s *CollectorServiceImpl) RegisterCollectorGateway(baseURL string) {
	// 注册网关处理器到全局网关系统
	gatewayHandler := NewGatewayHandler(s)
	RegisterGatewayHandler(gatewayHandler)
}

// Start 启动服务
func (s *CollectorServiceImpl) Start(ctx context.Context) {
	// 启动服务管理器
	if s.serviceManager != nil {
		s.serviceManager.Start(ctx)
	}

	// 启动心跳管理器
	if s.heartbeatManager != nil {
		s.heartbeatManager.Start(ctx)
	}
}

// Stop 停止服务
func (s *CollectorServiceImpl) Stop() {
	if s.serviceManager != nil {
		s.serviceManager.Stop()
	}
}

// SetCloudProvider 设置云提供商
func (s *CollectorServiceImpl) SetCloudProvider(p provider.CloudProvider) {
	s.cloudProvider = p

	// 设置心跳管理器的云提供商
	if s.heartbeatManager != nil {
		s.heartbeatManager.SetCloudProvider(p)
	}
}

// RegisterHTTPRoutes 注册HTTP路由（用于文件上传等特殊接口）
func (s *CollectorServiceImpl) RegisterHTTPRoutes(mux *http.ServeMux) {
	// 注册云节点HTTP路由（文件上传、云函数调用等）
	cloudnodeapi.RegisterCloudNodeHTTPRoutes(mux, s.db, s.queueManager)

	log.Info("[Collector Service] HTTP路由注册完成")
}

// GetHeartbeatManager 获取心跳管理器
func (s *CollectorServiceImpl) GetHeartbeatManager() *nodeheartbeat.Manager {
	return s.heartbeatManager
}

// GetCloudProvider 获取云提供商
func (s *CollectorServiceImpl) GetCloudProvider() provider.CloudProvider {
	return s.cloudProvider
}

// GetAsyncTaskService 获取异步任务服务
func (s *CollectorServiceImpl) GetAsyncTaskService() asynctasklogic.AsyncTaskService {
	return s.asyncTaskService
}
