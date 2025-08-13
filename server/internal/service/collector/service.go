package collector

import (
	"github.com/mooyang-code/moox/server/internal/service/collector/api"
	"github.com/mooyang-code/moox/server/internal/service/collector/logic"
	"gorm.io/gorm"
)

// CollectorService 采集器服务总接口
type CollectorService interface {
	logic.CollectorTaskConfigService
	logic.CollectorTaskInstanceService
}

type collectorServiceImpl struct {
	logic.CollectorTaskConfigService
	logic.CollectorTaskInstanceService
}

// NewCollectorService 创建采集器服务
func NewCollectorService(db *gorm.DB) CollectorService {
	return &collectorServiceImpl{
		CollectorTaskConfigService:   logic.NewCollectorTaskConfigService(db),
		CollectorTaskInstanceService: logic.NewCollectorTaskInstanceService(db),
	}
}


// NewCollectorTaskConfigHandler 创建采集任务配置处理器（导出给 init.go 使用）
func NewCollectorTaskConfigHandler(db *gorm.DB) api.SchemaHandler {
	return api.NewCollectorTaskConfigHandler(db)
}

// NewCollectorTaskInstanceHandler 创建采集任务实例处理器（导出给 init.go 使用）
func NewCollectorTaskInstanceHandler(db *gorm.DB) api.SchemaHandler {
	return api.NewCollectorTaskInstanceHandler(db)
}

