package marketwiring

import (
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

// FieldOffers 返回行情源目录里全部可用的附加字段，供建任务校验、结果数据集建列、任务表单、深度检查使用。
func FieldOffers() marketdata.FieldOffers { return catalog.FieldOffers() }

// FieldSources 构造（市场, 品种）下全部提供附加字段的来源，供采集管线取数。
// 来源的运行参数（限速等）按数据源 ID 从市场的数据源配置文件（config/markets/<市场>/sources）加载，没有配置文件的用默认值。
func FieldSources(marketID string, instrumentType marketdata.InstrumentType) ([]marketdata.FieldSource, error) {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	return catalog.Default().FieldSources(marketdata.MarketID(marketID), instrumentType, func(entry marketdata.CatalogEntry) (marketdata.Deps, error) {
		deps := marketdata.Deps{HTTPClient: httpclient.SharedClientIfEnabled()}
		runtime, found, err := marketfetch.LoadSourceRuntime(marketID, entry.Key.ProviderID)
		if err != nil {
			return marketdata.Deps{}, err
		}
		if found {
			deps.Settings = sourceSettings(runtime)
		}
		return deps, nil
	})
}

// bindFields 给管线接上它的（市场, 品种）下的附加字段来源。
func bindFields(pipeline *marketfetch.KlinePipeline) error {
	sources, err := FieldSources(pipeline.MarketID, pipeline.InstrumentType)
	if err != nil {
		return err
	}
	pipeline.Fields = sources
	for _, spec := range FieldOffers().Specs(pipeline.MarketID) {
		pipeline.ExtraFieldIDs = append(pipeline.ExtraFieldIDs, spec.ID)
	}
	return nil
}

// FundingCacheForMarket 返回计划市场的行情源在本进程里唯一的资金费率缓存（collector-stream 的推送用）；
// 行情源没有资金费率能力时返回 nil。
func FundingCacheForMarket(market marketfetch.PlanMarket) *marketfetch.FundingCache {
	providerID, sourceID := market.RequestSource()
	entry, err := catalog.Default().Lookup(marketdata.MarketID(market.MarketID), marketdata.InstrumentType(market.InstrumentType), providerID, sourceID)
	if err != nil || entry.RouteSource {
		return nil
	}
	provider, err := entry.Build(marketdata.Deps{HTTPClient: httpclient.SharedClientIfEnabled()})
	if err != nil {
		return nil
	}
	if holder, ok := provider.(interface {
		FundingCache() *marketfetch.FundingCache
	}); ok {
		return holder.FundingCache()
	}
	return nil
}
