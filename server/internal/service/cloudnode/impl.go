package cloudnode

import (
	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	"github.com/mooyang-code/moox/server/internal/service/database"

	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceImpl 实现 Service 接口（包含 NodeService, AccountService, PackageService, HeartbeatService）
type ServiceImpl struct {
	config *config.Config

	nodeDAO      dao.CloudNodeDAO
	accountDAO   dao.CloudAccountDAO
	packageDAO   dao.FunctionPackageDAO
	heartbeatDAO dao.HeartbeatDAO

	asyncTask       asynctask.Service
	providerFactory *provider.AccountFactory
}

// init 初始化服务（延迟初始化）
func (s *ServiceImpl) init() {
	if s.providerFactory == nil {
		s.providerFactory = provider.NewAccountFactory(s)
	}
}

// NewService 创建云节点服务实例
func NewService(dbManager *database.Manager, asyncTask asynctask.Service, cfg *config.Config) Service {
	db := dbManager.GetDB()

	serviceImpl := &ServiceImpl{
		config:       cfg,
		nodeDAO:      dao.NewCloudNodeDAO(db),
		accountDAO:   dao.NewCloudAccountDAO(db),
		packageDAO:   dao.NewFunctionPackageDAO(db),
		asyncTask:    asyncTask,
		heartbeatDAO: dao.NewHeartbeatNodeDAO(db),
	}

	// 初始化providerFactory
	serviceImpl.init()

	// 注册默认探测器到全局注册表
	err := RegisterDefaultProbers(serviceImpl.nodeDAO, serviceImpl.providerFactory)
	if err != nil {
		log.Errorf("register default probers failed: %v", err)
		return nil
	}
	return serviceImpl
}

// getRegionTag 根据地区代码从配置中获取标签（国内/海外）
func (s *ServiceImpl) getRegionTag(region string) string {
	if s.config == nil {
		return ""
	}

	// 目前只支持腾讯云
	for _, r := range s.config.CloudRegions.Tencent {
		if r.Code == region {
			return r.Tag
		}
	}

	return ""
}
