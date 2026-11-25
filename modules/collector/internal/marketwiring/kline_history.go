package marketwiring

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

// KlineLookback 返回计划市场这个频率的 K 线最多能往回取多久（按调用时刻算，见 KlineHistoryCapability.ValidateStart），
// 与取数时的装配口径一致（见 NewKlinePipeline）：按路由分配来源的取候选链上各来源的最大值（起点超出一家的回看时，
// 取数沿候选链换下一家），否则取任务指定的那一个来源。有来源支持任意区间时 bounded 为假（不限）。
// 只读能力声明：不注入 HTTP 客户端，构造不发请求。
func KlineLookback(market marketfetch.PlanMarket, frequency string) (lookback time.Duration, bounded bool, err error) {
	marketID := strings.ToLower(strings.TrimSpace(market.MarketID))
	instrument := marketdata.InstrumentType(strings.ToLower(strings.TrimSpace(market.InstrumentType)))
	parsed, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return 0, false, err
	}
	type source struct{ provider, sourceID string }
	var sources []source
	if catalog.Default().Routed(marketdata.MarketID(marketID), instrument) {
		route, found, err := marketfetch.RouteFor(marketID, instrument)
		if err != nil {
			return 0, false, err
		}
		if !found {
			return 0, false, fmt.Errorf("%s 的 %s 数据源登记为按路由分配，但路由文件里没有这个品种", marketID, instrument)
		}
		kline, err := route.Kline(string(parsed))
		if err != nil {
			return 0, false, err
		}
		for _, item := range kline.Sources() {
			sources = append(sources, source{provider: item.Provider, sourceID: item.SourceID})
		}
	} else {
		sources = append(sources, source{provider: strings.ToLower(strings.TrimSpace(market.Provider))})
	}
	for _, item := range sources {
		entry, err := catalog.Default().Lookup(marketdata.MarketID(marketID), instrument, item.provider, item.sourceID)
		if err != nil {
			return 0, false, fmt.Errorf("行情源目录里没有 %s 的 %s 数据源 %q", marketID, instrument, item.provider)
		}
		provider, err := entry.Build(marketdata.Deps{SourceID: item.sourceID})
		if err != nil {
			return 0, false, err
		}
		fetcher, ok := provider.(marketdata.KlineFetcher)
		if !ok {
			return 0, false, fmt.Errorf("%s 的 %s 数据源 %q 不提供 K 线", marketID, instrument, item.provider)
		}
		history := fetcher.KlineSpec().HistoryFor(string(parsed))
		if history.SupportsArbitraryRange {
			return 0, false, nil
		}
		lookback = max(lookback, history.MaxLookback)
	}
	return lookback, true, nil
}
