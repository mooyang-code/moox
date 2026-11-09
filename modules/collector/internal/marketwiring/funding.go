package marketwiring

import (
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

var (
	fundingMu     sync.Mutex
	fundingCaches = map[string]*marketfetch.FundingCache{}
)

// FundingCacheFor 返回这个行情源在本进程里唯一的资金费率缓存；行情源没有资金费率能力时返回 nil。
// 缓存只在第一次用到时构造：控制机上的 collector 不跑拉取管线，不会建；collector-stream 与 SCF 按需建。
func FundingCacheFor(marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string) *marketfetch.FundingCache {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	instrumentType = marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(string(instrumentType))))
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	sourceID = strings.ToLower(strings.TrimSpace(sourceID))
	if sourceID == "" {
		sourceID = DefaultSourceID(providerID, string(instrumentType))
	}
	key := strings.Join([]string{marketID, string(instrumentType), providerID, sourceID}, "/")
	fundingMu.Lock()
	defer fundingMu.Unlock()
	if cache, ok := fundingCaches[key]; ok {
		return cache
	}
	var cache *marketfetch.FundingCache
	if source := fundingSource(marketID, instrumentType, providerID, sourceID); source != nil {
		cache = marketfetch.NewFundingCache(source, nil)
	}
	fundingCaches[key] = cache
	return cache
}

// FundingCacheForMarket 按计划市场取资金费率缓存（collector-stream 的推送与本地拉取共用）。
func FundingCacheForMarket(market marketfetch.PlanMarket) *marketfetch.FundingCache {
	provider, sourceID := market.RequestSource()
	return FundingCacheFor(market.MarketID, marketdata.InstrumentType(market.InstrumentType), provider, sourceID)
}

// SupportsFundingRate 判断（空间, 行情源, 市场类型）的行情源是否具备资金费率能力，供建任务、改任务校验使用。
func SupportsFundingRate(spaceID, providerID, marketType string) bool {
	return fundingSource(strings.ToLower(strings.TrimSpace(spaceID)), marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(marketType))), strings.ToLower(strings.TrimSpace(providerID)), "") != nil
}

func fundingSource(marketID string, instrumentType marketdata.InstrumentType, providerID, sourceID string) marketdata.FundingRateSource {
	entry, err := catalog.Default().Lookup(marketdata.MarketID(marketID), instrumentType, providerID, sourceID)
	if err != nil || entry.RouteSource {
		return nil
	}
	provider, err := entry.Build(marketdata.Deps{HTTPClient: httpclient.SharedClientIfEnabled()})
	if err != nil {
		return nil
	}
	return marketdata.FundingRatesOf(provider)
}
