package collector

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"
	cloudaccountlogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	cloudaccountmodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	cloudnodequeue "github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	nodeheartbeat "github.com/mooyang-code/moox/server/internal/service/nodeservice/heartbeat"
	packagemgrexecutor "github.com/mooyang-code/moox/server/internal/service/packagemgr/executor"
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
	cloudProvider    provider.Client                   // 默认云提供商（保持兼容性）
	cosProviders     map[string]provider.ClientWithCOS // accountID -> COS提供商
	queueManager     *cloudnodequeue.QueueManager
	heartbeatManager *nodeheartbeat.Manager
	asyncTaskService asynctasklogic.AsyncTaskService
}

// InitCollectorServiceImpl 初始化采集器服务实现
func InitCollectorServiceImpl(dbPath string) (*CollectorServiceImpl, error) {
	impl := &CollectorServiceImpl{
		cosProviders: make(map[string]provider.ClientWithCOS),
	}

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

	// 初始化服务管理器（暂时传入nil作为cosProvider，稍后会设置）
	impl.serviceManager = NewServiceManager(db, impl.queueManager, nil)

	// 初始化云提供商
	impl.initCloudProviders()

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

// initCloudProviders 初始化云提供商
func (s *CollectorServiceImpl) initCloudProviders() {
	ctx := context.Background()

	// 创建云账户服务
	cloudAccountService := cloudaccountlogic.NewCloudAccountService(s.db)

	// 获取腾讯云账户列表（支持COS的）
	tencentAccounts, err := cloudAccountService.ListAccountsByProvider(ctx, cloudaccountmodel.CloudProviderTencent)
	if err != nil {
		log.Warnf("[Collector Service] 获取腾讯云账户失败: %v，将跳过云提供商初始化", err)
		return
	}

	if len(tencentAccounts) == 0 {
		log.Warn("[Collector Service] 未找到腾讯云账户配置，将跳过云提供商初始化")
		return
	}

	var defaultProvider provider.Client
	cosProviderCount := 0

	// 遍历所有腾讯云账户，为每个有COS配置的账户创建COS客户端
	for _, account := range tencentAccounts {
		// 获取不脱敏的账户信息
		fullAccount, err := cloudAccountService.GetAccountWithoutMask(ctx, account.AccountID)
		if err != nil {
			log.Warnf("[Collector Service] 获取云账户(%s)详情失败: %v，跳过该账户", account.AccountID, err)
			continue
		}

		// 检查是否有COS配置
		if fullAccount.COSBucket == "" {
			log.Warnf("[Collector Service] 云账户(%s)未配置COS桶，跳过该账户", fullAccount.AccountName)
			continue
		}

		// 构建额外配置
		extraConfig := fmt.Sprintf(`{"region":"%s","cos_bucket":"%s","cos_app_id":"%s"}`,
			fullAccount.COSRegion, fullAccount.COSBucket, fullAccount.AppID)

		// 创建云平台配置
		cloudConfig, err := provider.NewConfig(
			provider.Tencent,
			fullAccount.SecretID,
			fullAccount.SecretKey,
			extraConfig,
		)
		if err != nil {
			log.Warnf("[Collector Service] 创建云配置失败(%s): %v，跳过该账户", fullAccount.AccountName, err)
			continue
		}

		// 创建支持COS的腾讯云提供商
		cosProvider, err := provider.NewTencentWrapperWithCOS(cloudConfig)
		if err != nil {
			log.Warnf("[Collector Service] 创建COS提供商失败(%s): %v，跳过该账户", fullAccount.AccountName, err)
			continue
		}

		// 将COS提供商加入到map中
		s.cosProviders[fullAccount.AccountID] = cosProvider
		cosProviderCount++

		// 如果是第一个成功的账户，设置为默认云提供商（保持兼容性）
		if defaultProvider == nil {
			defaultProvider = cosProvider
			s.SetCloudProvider(cosProvider)
			// 设置服务管理器的COS提供商
			if s.serviceManager != nil {
				s.serviceManager.SetCOSProvider(cosProvider)
			}
		}

		log.Infof("[Collector Service] COS提供商初始化完成，账户: %s (COS桶: %s)",
			fullAccount.AccountName, fullAccount.COSBucket)
	}

	if cosProviderCount == 0 {
		log.Warn("[Collector Service] 未找到有效的COS配置，将跳过云提供商初始化")
		return
	}
	log.Infof("[Collector Service] 云提供商初始化完成，共初始化 %d 个COS客户端", cosProviderCount)
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

	var packageService *packagemgrlogic.FunctionPackageService
	if s.cloudProvider == nil {
		log.Warn("[Collector Service] CloudProvider 未设置，跳过执行器注册")
		return
	}

	// 创建包管理服务（用于批量部署）
	if cosProvider, ok := s.cloudProvider.(provider.ClientWithCOS); ok {
		cosBucket := "moox-packages" // 从配置中获取
		packageService = packagemgrlogic.NewFunctionPackageService(s.db, cosProvider, cosBucket)
	} else {
		log.Warn("[Collector Service] CloudProvider 不支持COS功能，跳过包管理服务初始化")
	}

	// 创建[批量部署节点]执行器
	batchDeployNodeExecutor := logic.NewBatchDeployNodeExecutor(s.db, scfNodeService, asyncTaskService, packageService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(batchDeployNodeExecutor.GetTaskType(), batchDeployNodeExecutor)

	// 创建[代码包上传]执行器
	cosProvider, ok := s.cloudProvider.(provider.ClientWithCOS)
	if !ok {
		log.Warn("[Collector Service] CloudProvider 不支持COS功能，跳过代码包上传执行器注册")
		return
	}
	packageUploadExecutor := packagemgrexecutor.NewPackageUploadExecutor(s.db, cosProvider, asyncTaskService)

	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(packageUploadExecutor.GetTaskType(), packageUploadExecutor)
	log.Info("[Collector Service] 代码包上传执行器注册完成")

	log.Info("[Collector Service] 采集器处理器注册完成，已注册任务执行器：" +
		"BATCH_CREATE_NODE, BATCH_DELETE_NODE, BATCH_DEPLOY_NODE, UPLOAD_PACKAGE_TO_COS")
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
func (s *CollectorServiceImpl) SetCloudProvider(p provider.Client) {
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
func (s *CollectorServiceImpl) GetCloudProvider() provider.Client {
	return s.cloudProvider
}

// GetAsyncTaskService 获取异步任务服务
func (s *CollectorServiceImpl) GetAsyncTaskService() asynctasklogic.AsyncTaskService {
	return s.asyncTaskService
}

// GetCOSProvider 根据账户ID获取COS提供商
func (s *CollectorServiceImpl) GetCOSProvider(accountID string) provider.ClientWithCOS {
	if accountID == "" {
		// 如果没有指定账户ID，返回默认云提供商（如果支持COS）
		if cosProvider, ok := s.cloudProvider.(provider.ClientWithCOS); ok {
			return cosProvider
		}
		return nil
	}
	return s.cosProviders[accountID]
}

// GetAllCOSProviders 获取所有COS提供商
func (s *CollectorServiceImpl) GetAllCOSProviders() map[string]provider.ClientWithCOS {
	return s.cosProviders
}
