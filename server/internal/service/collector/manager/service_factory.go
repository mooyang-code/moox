package manager

import (
	cloudnodedao "github.com/mooyang-code/moox/server/internal/service/cloudnode/dao"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode/provider"
	collectordao "github.com/mooyang-code/moox/server/internal/service/collector/dao"
	collectorlogic "github.com/mooyang-code/moox/server/internal/service/collector/impl"
	"github.com/mooyang-code/moox/server/internal/service/database"
	"gorm.io/gorm"
)

// ServiceFactory 服务工厂，负责创建采集器相关的组件
type ServiceFactory struct {
	dbManager *database.Manager
}

// NewServiceFactory 创建服务工厂
func NewServiceFactory(dbManager *database.Manager) *ServiceFactory {
	return &ServiceFactory{
		dbManager: dbManager,
	}
}

// GetDB 获取数据库连接（供bootstrap等外部模块使用）
func (f *ServiceFactory) GetDB() *gorm.DB {
	return f.dbManager.GetDB()
}

// CreateCollectorTaskInstanceDAO 创建采集任务实例DAO
func (f *ServiceFactory) CreateCollectorTaskInstanceDAO() collectordao.CollectorTaskInstanceDAO {
	return collectordao.NewCollectorTaskInstanceDAO(f.dbManager.GetDB())
}

// CreateCollectorTaskConfigDAO 创建采集任务配置DAO
func (f *ServiceFactory) CreateCollectorTaskConfigDAO() collectordao.CollectorTaskConfigDAO {
	return collectordao.NewCollectorTaskConfigDAO(f.dbManager.GetDB())
}

// CreateCloudNodeDAO 创建云节点DAO
func (f *ServiceFactory) CreateCloudNodeDAO() cloudnodedao.CloudNodeDAO {
	return cloudnodedao.NewCloudNodeDAO(f.dbManager.GetDB())
}

// CreateHeartbeatDAO 创建心跳DAO
func (f *ServiceFactory) CreateHeartbeatDAO() cloudnodedao.HeartbeatDAO {
	return cloudnodedao.NewHeartbeatNodeDAO(f.dbManager.GetDB())
}

// CreateTaskInstanceService 创建采集任务实例服务
func (f *ServiceFactory) CreateTaskInstanceService() collectorlogic.TaskInstanceService {
	instanceDAO := f.CreateCollectorTaskInstanceDAO()
	taskConfigDAO := f.CreateCollectorTaskConfigDAO()
	nodeDAO := f.CreateCloudNodeDAO()
	heartbeatDAO := f.CreateHeartbeatDAO()
	return collectorlogic.NewTaskInstanceService(instanceDAO, taskConfigDAO, nodeDAO, heartbeatDAO)
}

// CreateTaskConfigService 创建采集任务配置服务
func (f *ServiceFactory) CreateTaskConfigService(getCloudProvider func(string) provider.Client) collectorlogic.TaskConfigService {
	taskConfigDAO := f.CreateCollectorTaskConfigDAO()
	nodeDAO := f.CreateCloudNodeDAO()
	return collectorlogic.NewTaskConfigService(taskConfigDAO, nodeDAO, getCloudProvider)
}
