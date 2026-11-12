package marketfetch

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

// PlanFleetMarker 标记由采集计划对账管理的定时函数（名称中包含该片段）。
const PlanFleetMarker = "-plan-"

// fixedSeriesTag 返回市场在 market.yaml 里配的固定序列标签（stockcn 是 default）；没配或市场未登记时为空，表示按数据源生成。
func fixedSeriesTag(marketID string) string {
	config, err := markets.ConfigFor(marketID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(config.SeriesTag)
}

// realtimeBatchNoCap 报告市场的实时批次是否不限标的数（market.yaml 的 realtime_batch_no_cap）；市场未登记时按有上限处理。
func realtimeBatchNoCap(marketID string) bool {
	config, err := markets.ConfigFor(marketID)
	return err == nil && config.RealtimeBatchNoCap
}

// collectionItemSeriesTag 返回行情序列的 series_tag：市场配了固定标签的用它，否则按数据源生成。
func collectionItemSeriesTag(spaceID string, item domain.CollectionItem) string {
	if tag := fixedSeriesTag(spaceID); tag != "" {
		return tag
	}
	if strings.TrimSpace(item.Provider) == "" && strings.TrimSpace(item.SourceID) == "" {
		return ""
	}
	return defaultMarketSeriesTag(item.Provider, item.SourceID, item.MarketType)
}
