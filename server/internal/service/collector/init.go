package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"net/http"

	asynctaskapi "github.com/mooyang-code/moox/server/internal/service/asynctask/api"
	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	cloudnodequeue "github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	nodeheartbeat "github.com/mooyang-code/moox/server/internal/service/nodeservice/heartbeat"
	"github.com/mooyang-code/moox/server/internal/service/collector/api"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	"trpc.group/trpc-go/trpc-go/log"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// CollectorServiceImpl 采集器服务实现
type CollectorServiceImpl struct {
	db               *gorm.DB
	serviceManager   *ServiceManager
	httpMux          *http.ServeMux
	cloudProvider    provider.CloudProvider
	queueManager     *cloudnodequeue.QueueManager
	heartbeatManager *nodeheartbeat.Manager
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

// RegisterCollectorHandlers 注册采集器处理器（http API接口）
func (s *CollectorServiceImpl) RegisterCollectorHandlers() {
	// 采集器专用处理器
	taskConfigHandler := api.NewCollectorTaskConfigHandler(s.db)
	taskInstanceHandler := api.NewCollectorTaskInstanceHandler(s.db)

	// 注册到API系统
	RegisterHandler(taskConfigHandler)
	RegisterHandler(taskInstanceHandler)

	// 注册CloudNode相关的处理器
	// 节点管理处理器（使用带队列管理器的服务）
	scfNodeService := cloudnodelogic.NewSCFNodeServiceWithQueue(s.db, s.queueManager)
	nodeHandler := cloudnodeapi.NewSCFNodeHandlerWithService(scfNodeService)
	RegisterCloudNodeHandler(nodeHandler)

	// 云账户管理处理器
	accountHandler := cloudnodeapi.NewCloudAccountSchemaHandler(s.db)
	RegisterCloudNodeHandler(accountHandler)
}

// RegisterCollectorGateway 注册采集器网关（注册到网关接口）
func (s *CollectorServiceImpl) RegisterCollectorGateway(baseURL string) {
	// 注册网关处理器到全局网关系统
	gatewayHandler := NewGatewayHandler(baseURL)
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
	// 注册异步任务服务路由
	asyncTaskService := asynctasklogic.NewAsyncTaskService(s.db)

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

	// 创建批量部署节点执行器
	batchDeployNodeExecutor := logic.NewBatchDeployNodeExecutor(s.db, scfNodeService, asyncTaskService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(batchDeployNodeExecutor.GetTaskType(), batchDeployNodeExecutor)

	// 使用服务实例初始化处理器
	asyncTaskHandlerWithService := asynctaskapi.NewAsyncTaskHandlerWithService(asyncTaskService)
	asyncTaskHandlerWithService.RegisterRoutes(mux)

	// 注册云节点HTTP路由（文件上传、云函数调用等）
	cloudnodeapi.RegisterCloudNodeHTTPRoutes(mux, s.db, s.queueManager)

	log.Info("[Collector Service] HTTP路由注册完成，已注册任务执行器：BATCH_CREATE_NODE, BATCH_DELETE_NODE, BATCH_DEPLOY_NODE")
}

// GetHeartbeatManager 获取心跳管理器
func (s *CollectorServiceImpl) GetHeartbeatManager() *nodeheartbeat.Manager {
	return s.heartbeatManager
}
