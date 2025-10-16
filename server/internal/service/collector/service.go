package collector

import (
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


