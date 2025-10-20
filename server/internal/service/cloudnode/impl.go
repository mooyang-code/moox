package cloudnode

import (
	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/database"

	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceImpl 实现 Service 接口（包含 NodeService, AccountService, PackageService, HeartbeatService）
type ServiceImpl struct {
	db     *gorm.DB
	config *config.Config

	nodeDAO      dao.CloudNodeDAO
	accountDAO   dao.CloudAccountDAO
	packageDAO   dao.FunctionPackageDAO
	heartbeatDAO dao.HeartbeatDAO

	asyncTask       asynctask.Service
	providerFactory *provider.AccountFactory
	heartbeatProber *HeartbeatProber
}

// init 初始化服务（延迟初始化）
func (s *ServiceImpl) init() {
	if s.providerFactory == nil {
		s.providerFactory = provider.NewAccountFactory(s)
	}
}

// NewService 创建云节点服务实例
func NewService(dbManager *database.Manager, asyncTask asynctask.Service) Service {
	db := dbManager.GetDB()

	// 加载服务配置
	cfg := config.LoadConfig()

	serviceImpl := &ServiceImpl{
		db:           db,
		config:       cfg,
		nodeDAO:      dao.NewCloudNodeDAO(db),
		accountDAO:   dao.NewCloudAccountDAO(db),
		packageDAO:   dao.NewFunctionPackageDAO(db),
		asyncTask:    asyncTask,
		heartbeatDAO: dao.NewHeartbeatRecordDAO(db),
	}

	// 初始化providerFactory
	serviceImpl.init()

	// 注册默认探测器到全局注册表
	err := RegisterDefaultProbers(serviceImpl.nodeDAO, serviceImpl.providerFactory)
	if err != nil {
		log.Errorf("register default probers failed: %v", err)
		return nil
	}

	// 创建心跳服务核心组件，传入探测器配置
	serviceImpl.heartbeatProber = NewProber(serviceImpl.heartbeatDAO, &cfg.Prober)

	// 将全局注册表中的探测器注册到prober
	for _, proberInstance := range ListProbers() {
		serviceImpl.heartbeatProber.RegisterProber(proberInstance)
	}
	return serviceImpl
}
