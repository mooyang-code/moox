package cloudnode

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/config"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collectmgr/dao"
	"github.com/mooyang-code/moox/server/internal/service/database"

	"trpc.group/trpc-go/trpc-go/log"
)

// ServiceImpl 实现 Service 接口（包含 NodeService, AccountService, PackageService, HeartbeatService）
type ServiceImpl struct {
	config *config.Config

	nodeDAO         dao.CloudNodeDAO
	accountDAO      dao.CloudAccountDAO
	packageDAO      dao.FunctionPackageDAO
	taskInstanceDAO collectordao.CollectorTaskInstanceDAO

	asyncTask       asynctask.Service
	providerFactory *provider.AccountFactory
	heartbeatStore  *HeartbeatStore // 心跳内存存储
	probeStore      *ProbeStore     // 保活探测内存存储
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
		config:          cfg,
		nodeDAO:         dao.NewCloudNodeDAO(db),
		accountDAO:      dao.NewCloudAccountDAO(db),
		packageDAO:      dao.NewFunctionPackageDAO(db),
		taskInstanceDAO: collectordao.NewCollectorTaskInstanceDAO(db),
		asyncTask:       asyncTask,
		heartbeatStore:  NewHeartbeatStore(), // 使用内存存储
		probeStore:      NewProbeStore(),
	}

	// 初始化providerFactory
	serviceImpl.init()

	return serviceImpl
}

func (s *ServiceImpl) getRegionInfoByProvider(provider string, region string) *config.RegionInfo {
	if s.config == nil {
		return nil
	}

	// 多云支持入口：新增厂商时在这里扩展分支，并同步完善 config.CloudRegions 的配置结构。
	switch strings.ToLower(provider) {
	case "tencent":
		for i := range s.config.CloudRegions.Tencent {
			if s.config.CloudRegions.Tencent[i].Code == region {
				return &s.config.CloudRegions.Tencent[i]
			}
		}
	default:
		return nil
	}

	return nil
}

func (s *ServiceImpl) getRegionInfoByAccount(ctx context.Context, accountID, region string) (*config.RegionInfo, error) {
	if accountID == "" {
		return nil, fmt.Errorf("cloud account id is required")
	}

	account, err := s.accountDAO.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get cloud account: %w", err)
	}
	if account == nil {
		return nil, fmt.Errorf("cloud account not found")
	}

	info := s.getRegionInfoByProvider(account.Provider, region)
	if info == nil {
		return nil, fmt.Errorf("region %s not found for provider %s", region, account.Provider)
	}
	return info, nil
}

// getRegionTag 根据地区代码从配置中获取标签（国内/海外）
func (s *ServiceImpl) getRegionTagByAccount(ctx context.Context, accountID, region string) string {
	info, err := s.getRegionInfoByAccount(ctx, accountID, region)
	if err != nil {
		log.WarnContextf(ctx, "[CloudNode] Failed to get region tag: %v", err)
		return ""
	}
	return info.Tag
}
