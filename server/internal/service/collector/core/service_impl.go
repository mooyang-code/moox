package core

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"
	cloudaccountlogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	cloudnodequeue "github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	nodeheartbeat "github.com/mooyang-code/moox/server/internal/service/nodeservice/heartbeat"
	packagemgrdao "github.com/mooyang-code/moox/server/internal/service/packagemgr/dao"
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
	cloudProviders   map[string]provider.Client // accountID -> 云厂商客户端
	queueManager     *cloudnodequeue.Manager
	heartbeatManager *nodeheartbeat.Manager
	asyncTaskService asynctask.Service
}

// InitCollectorServiceImpl 初始化采集器服务实现
func InitCollectorServiceImpl(dbPath string) (*CollectorServiceImpl, error) {
	impl := &CollectorServiceImpl{
		cloudProviders: make(map[string]provider.Client),
	}

	// 初始化数据库连接
	db, err := impl.initDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	impl.db = db

	// 初始化队列管理器
	impl.queueManager = cloudnodequeue.NewManager(db)

	// 初始化云节点服务（用于心跳管理器）
	nodeService := cloudnodelogic.NewSCFNodeService(db)

	// 初始化心跳服务（用于心跳管理器）
	heartbeatService := cloudnodelogic.NewHeartbeatService(db)

	// 初始化异步任务服务
	impl.asyncTaskService = asynctasklogic.NewService(db)

	// 初始化服务管理器（暂时传入nil作为cosProvider，稍后会设置）
	impl.serviceManager = NewServiceManager(db, impl.queueManager, nil)

	// 初始化云提供商（必须在心跳管理器之前）
	impl.initCloudProviders()

	// 初始化心跳管理器（传递获取云客户端的回调函数）
	impl.heartbeatManager = nodeheartbeat.NewManager(db, nodeService,
		heartbeatService, "ip://43.132.204.177:20001", impl.GetCloudProviderByAccount)
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

	// 获取所有云账户列表
	accounts, err := cloudAccountService.ListAccounts(ctx)
	if err != nil {
		log.Warnf("[Collector Service] 获取云账户列表失败: %v，将跳过云提供商初始化", err)
		return
	}
	if len(accounts) == 0 {
		log.Warn("[Collector Service] 未找到云账户配置，将跳过云提供商初始化")
		return
	}

	successCount := 0

	// 遍历所有云账户，使用工厂方法创建相应的云厂商客户端
	for _, account := range accounts {
		// 跳过无效账户
		if account.Provider == "" {
			log.Warnf("[Collector Service] 云账户(%s)未配置云平台类型，跳过该账户", account.AccountName)
			continue
		}

		// 获取不脱敏的账户信息
		fullAccount, err := cloudAccountService.GetAccountWithoutMask(ctx, account.AccountID)
		if err != nil {
			log.Warnf("[Collector Service] 获取云账户(%s)详情失败: %v，跳过该账户", account.AccountID, err)
			continue
		}

		// 解析云平台类型
		platformType, err := provider.ParseCloudPlatform(fullAccount.Provider)
		if err != nil {
			log.Warnf("[Collector Service] 不支持的云平台类型(%s): %v，跳过该账户", fullAccount.Provider, err)
			continue
		}

		// 构建额外配置
		region := fullAccount.COSRegion
		extraConfig := fmt.Sprintf(`{"region":"%s"}`, region)
		if fullAccount.COSBucket != "" && fullAccount.COSRegion != "" {
			extraConfig = fmt.Sprintf(`{"region":"%s","cos_bucket":"%s","cos_region":"%s","cos_app_id":"%s"}`,
				region, fullAccount.COSBucket, fullAccount.COSRegion, fullAccount.AppID)
		}

		// 创建云平台配置
		cloudConfig, err := provider.NewConfig(
			platformType,
			fullAccount.SecretID,
			fullAccount.SecretKey,
			extraConfig,
		)
		if err != nil {
			log.Warnf("[Collector Service] 创建云配置失败(%s): %v，跳过该账户", fullAccount.AccountName, err)
			continue
		}

		// 使用工厂方法创建云厂商客户端
		cloudClient, err := provider.New(cloudConfig)
		if err != nil {
			log.Warnf("[Collector Service] 创建云厂商客户端失败(%s): %v，跳过该账户", fullAccount.AccountName, err)
			continue
		}

		// 存储到 map 中
		s.cloudProviders[fullAccount.AccountID] = cloudClient
		successCount++
		log.Infof("[Collector Service] 云提供商初始化完成 - 平台: %s, 账户: %s, 账户ID: %s, 区域: %s",
			fullAccount.Provider, fullAccount.AccountName, fullAccount.AccountID, region)
	}

	if successCount == 0 {
		log.Warn("[Collector Service] 未找到有效的云账户配置，将跳过云提供商初始化")
		return
	}
	log.Infof("[Collector Service] 云提供商初始化完成，共初始化 %d 个云厂商客户端", successCount)
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
	// COS客户端不再需要预先传入，将在异步任务执行时动态获取
	packageDAO := packagemgrdao.NewFunctionPackageDAO(s.db)
	packageService := packagemgrlogic.NewFunctionPackageService(packageDAO)

	// 创建[批量部署节点]执行器
	batchDeployNodeExecutor := logic.NewBatchDeployNodeExecutor(s.db, scfNodeService, asyncTaskService, packageService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(batchDeployNodeExecutor.GetTaskType(), batchDeployNodeExecutor)

	// 创建[代码包上传]执行器（支持多云账户，COS客户端在执行时动态创建）
	packageUploadExecutor := packagemgrexecutor.NewPackageUploadExecutor(s.db, asyncTaskService)
	// 注册执行器到异步任务服务
	asyncTaskService.RegisterExecutor(packageUploadExecutor.GetTaskType(), packageUploadExecutor)
	log.Info("[Collector Service] 代码包上传执行器注册完成")

	log.Info("[Collector Service] 采集器处理器注册完成，已注册任务执行器：" +
		"BATCH_CREATE_NODE, BATCH_DELETE_NODE, BATCH_DEPLOY_NODE, UPLOAD_PACKAGE_TO_COS")
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

// GetCloudProviderByAccount 根据账户ID获取云厂商客户端
func (s *CollectorServiceImpl) GetCloudProviderByAccount(accountID string) provider.Client {
	// 必须指定账户ID
	if accountID == "" {
		log.Warn("[Collector Service] 账户ID为空，无法获取云厂商客户端")
		return nil
	}

	// 从 map 中获取
	if cloudClient, exists := s.cloudProviders[accountID]; exists {
		return cloudClient
	}

	// 未找到，返回 nil
	log.Warnf("[Collector Service] 未找到账户ID为 %s 的云厂商客户端", accountID)
	return nil
}

// GetAsyncTaskService 获取异步任务服务
func (s *CollectorServiceImpl) GetAsyncTaskService() asynctask.Service {
	return s.asyncTaskService
}
