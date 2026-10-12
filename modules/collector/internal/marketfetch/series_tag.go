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

// writeSourceForFunctionName 给 Storage 的写入事件一个稳定、可扩展的来源标识，
// 不让公开的事件契约永远绑定腾讯云 SCF。
func writeSourceForFunctionName(functionName string) string {
	functionName = strings.TrimSpace(functionName)
	if functionName == "" {
		return ""
	}
	return "scf:" + functionName
}
