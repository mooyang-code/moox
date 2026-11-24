package marketwiring

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
)

// CheckKlineFrequency 校验（市场，品种，来源）能采这个频率的 K 线，与取数时的装配口径一致（见 NewKlinePipeline）：
// 按路由分配来源的（市场，品种）看路由文件里有没有该频率；否则看行情源目录里该来源声明的 KlineSpec.Frequencies。
func CheckKlineFrequency(marketID, marketType, providerID, frequency string) error {
	marketID = strings.ToLower(strings.TrimSpace(marketID))
	rules, err := markets.RulesFor(marketID)
	if err != nil {
		return err
	}
	instrument := rules.DefaultInstrumentType(marketType)
	parsed, err := marketdata.ParseFrequency(frequency)
	if err != nil {
		return fmt.Errorf("不支持 %s K 线：%w", strings.TrimSpace(frequency), err)
	}
	if catalog.Default().Routed(marketdata.MarketID(marketID), instrument) {
		route, found, err := marketfetch.RouteFor(marketID, instrument)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s 的 %s 数据源登记为按路由分配，但路由文件里没有这个品种", marketID, instrument)
		}
		if _, err := route.Kline(string(parsed)); err != nil {
			return fmt.Errorf("%s 的 %s 不支持 %s K 线（路由文件里没有这个频率）", marketID, instrument, parsed)
		}
		return nil
	}
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	entry, err := catalog.Default().Lookup(marketdata.MarketID(marketID), instrument, providerID, "")
	if err != nil {
		return fmt.Errorf("行情源目录里没有 %s 的 %s 数据源 %q", marketID, instrument, providerID)
	}
	// 只读能力声明：不注入 HTTP 客户端，构造不发请求。
	provider, err := entry.Build(marketdata.Deps{})
	if err != nil {
		return err
	}
	fetcher, ok := provider.(marketdata.KlineFetcher)
	if !ok {
		return fmt.Errorf("%s 的 %s 数据源 %q 不提供 K 线", marketID, instrument, providerID)
	}
	supported := fetcher.KlineSpec().Frequencies
	for _, item := range supported {
		if strings.EqualFold(strings.TrimSpace(item), string(parsed)) {
			return nil
		}
	}
	return fmt.Errorf("%s 的 %s 数据源 %q 只支持 %s K 线，不支持 %s", marketID, instrument, providerID, strings.Join(supported, "、"), parsed)
}
