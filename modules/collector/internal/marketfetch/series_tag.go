package marketfetch

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
)

// PlanFleetMarker 标记由采集计划对账管理的定时函数（名称中包含该片段）。
const PlanFleetMarker = "-plan-"

// collectionItemSeriesTag 返回行情序列的 series_tag；stockcn 固定为 default。
func collectionItemSeriesTag(spaceID string, item domain.CollectionItem) string {
	if strings.EqualFold(strings.TrimSpace(spaceID), StockCNSpaceID) {
		return "default"
	}
	if strings.TrimSpace(item.Provider) == "" && strings.TrimSpace(item.SourceID) == "" {
		return ""
	}
	return defaultMarketSeriesTag(item.Provider, item.SourceID, item.MarketType)
}
