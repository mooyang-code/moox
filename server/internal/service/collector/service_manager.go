package collector

import (
	"context"

	asynctasklogic "github.com/mooyang-code/moox/server/internal/service/asynctask/logic"
	asynctaskworker "github.com/mooyang-code/moox/server/internal/service/asynctask/worker"
	cloudnodelogic "github.com/mooyang-code/moox/server/internal/service/cloudnode/logic"
	cloudnodequeue "github.com/mooyang-code/moox/server/internal/service/cloudnode/queue"
	cloudnodeworker "github.com/mooyang-code/moox/server/internal/service/cloudnode/worker"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	packagemgrlogic "github.com/mooyang-code/moox/server/internal/service/packagemgr/logic"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceManager manages all collector services
type ServiceManager struct {
	db                   *gorm.DB
	queueManager         *cloudnodequeue.QueueManager
	cosProvider          provider.ClientWithCOS
	asyncTaskWorker      *asynctaskworker.BaseWorker
	nodeCreationWorker   *cloudnodeworker.NodeCreationWorker
	nodeDeletionWorker   *cloudnodeworker.NodeDeletionWorker
	nodeDeploymentWorker *cloudnodeworker.NodeDeploymentWorker
}

// NewServiceManager creates a new service manager
func NewServiceManager(db *gorm.DB, queueManager *cloudnodequeue.QueueManager, cosProvider provider.ClientWithCOS) *ServiceManager {
	return &ServiceManager{
		db:           db,
		queueManager: queueManager,
		cosProvider:  cosProvider,
	}
}

// SetCOSProvider sets the COS provider for the service manager
func (m *ServiceManager) SetCOSProvider(cosProvider provider.ClientWithCOS) {
	m.cosProvider = cosProvider
}

// Start starts all services
func (m *ServiceManager) Start(ctx context.Context) {
	log.InfoContext(ctx, "[ServiceManager] Starting all services...")
	log.InfoContextf(ctx, "[ServiceManager] queueManager is nil: %v", m.queueManager == nil)
	
	// 启动异步任务worker
	// 注意：BaseWorker需要在collector/init.go中注册执行器后才能正常工作
	// 这里暂时不启动BaseWorker，因为执行器注册是在RegisterHTTPRoutes中完成的
	
	// 启动节点创建worker
	if m.queueManager != nil {
		// 创建必要的服务
		cloudAccountService := cloudnodelogic.NewCloudAccountService(m.db)
		asyncTaskService := asynctasklogic.NewAsyncTaskService(m.db)
		
		// 创建包管理服务
		var packageService cloudnodeworker.PackageService
		if m.cosProvider != nil {
			cosBucket := "moox-packages" // 应该从配置中获取
			functionPackageService := packagemgrlogic.NewFunctionPackageService(m.db, m.cosProvider, cosBucket)
			packageService = cloudnodeworker.NewPackageServiceAdapter(functionPackageService)
		} else {
			log.Warn("[ServiceManager] COS provider 未设置，将使用nil作为包管理服务")
		}
		
		// 创建并启动节点创建worker
		m.nodeCreationWorker = cloudnodeworker.NewNodeCreationWorker(m.db, m.queueManager, cloudAccountService, packageService, asyncTaskService)
		m.nodeCreationWorker.Start(ctx)
		log.InfoContext(ctx, "[ServiceManager] Node creation worker started")
		
		// 创建并启动节点删除worker
		log.InfoContext(ctx, "[ServiceManager] Creating node deletion worker...")
		m.nodeDeletionWorker = cloudnodeworker.NewNodeDeletionWorker(m.db, m.queueManager, cloudAccountService, asyncTaskService)
		log.InfoContext(ctx, "[ServiceManager] Starting node deletion worker...")
		m.nodeDeletionWorker.Start(ctx)
		log.InfoContext(ctx, "[ServiceManager] Node deletion worker started")
		
		// 创建并启动节点部署worker
		log.InfoContext(ctx, "[ServiceManager] Creating node deployment worker...")
		m.nodeDeploymentWorker = cloudnodeworker.NewNodeDeploymentWorker(m.db, m.queueManager, cloudAccountService, packageService, asyncTaskService)
		log.InfoContext(ctx, "[ServiceManager] Starting node deployment worker...")
		m.nodeDeploymentWorker.Start(ctx)
		log.InfoContext(ctx, "[ServiceManager] Node deployment worker started")
	}
	
	log.InfoContext(ctx, "[ServiceManager] All services started")
}

// Stop stops all services
func (m *ServiceManager) Stop() {
	log.Info("[ServiceManager] Stopping all services...")
	
	// 停止节点创建worker
	if m.nodeCreationWorker != nil {
		m.nodeCreationWorker.Stop()
		log.Info("[ServiceManager] Node creation worker stopped")
	}
	
	// 停止节点删除worker
	if m.nodeDeletionWorker != nil {
		m.nodeDeletionWorker.Stop()
		log.Info("[ServiceManager] Node deletion worker stopped")
	}
	
	// 停止节点部署worker
	if m.nodeDeploymentWorker != nil {
		m.nodeDeploymentWorker.Stop()
		log.Info("[ServiceManager] Node deployment worker stopped")
	}
	
	// 停止异步任务worker
	if m.asyncTaskWorker != nil {
		m.asyncTaskWorker.Stop()
		log.Info("[ServiceManager] Async task worker stopped")
	}
	
	log.Info("[ServiceManager] All services stopped")
}

