// Package marketwiring wires concrete data sources into common collection
// pipelines. Neither the scheduler nor marketfetch imports provider adapters.
package marketwiring

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
)

func NewHandler() *marketfetch.Handler {
	h := marketfetch.NewHandler()
	h.NewMarketKlinePipeline = NewMarketKlinePipeline
	h.NewCryptoKlinePipeline = NewCryptoKlinePipeline
	h.NewStockKlinePipeline = NewStockKlinePipeline
	h.ResolveSourceID = DefaultSourceID
	h.ResolveSymbol = ResolveSymbol
	h.PlanPeriodValid = planPeriodValid
	return h
}

func ResolveSymbol(provider, marketID, marketType, subjectID string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(provider), "binance") && strings.EqualFold(strings.TrimSpace(marketID), "crypto") &&
		(strings.EqualFold(strings.TrimSpace(marketType), "spot") || strings.EqualFold(strings.TrimSpace(marketType), "swap")) {
		return binance.ToSymbol(subjectID)
	}
	if strings.EqualFold(strings.TrimSpace(marketID), "crypto") {
		return "", fmt.Errorf("provider %q does not support crypto market symbol conversion", provider)
	}
	return marketfetch.DefaultProviderSymbol(marketID, marketType, subjectID)
}

func DefaultSourceID(provider, instrumentType string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "binance":
		switch strings.ToLower(strings.TrimSpace(instrumentType)) {
		case "spot":
			return "spot_http"
		case "swap":
			return "swap_http"
		}
	case "sina":
		return "stockcn_minute_http"
	case "eastmoney", "tencent", "baidu":
		return "stockcn_http"
	}
	return ""
}

// planPeriodValid 是采集计划的周期过滤：周期开始时刻必须是该市场此频率的一个交易桶。连续交易的市场每个周期都执行；
// 交易时段市场在午休、收盘后、非交易日不执行。交易日历判断不了（超出覆盖期）时也不执行，避免制造无意义的提交。
func planPeriodValid(market marketfetch.PlanMarket, frequency string, period time.Time) bool {
	trading, err := markets.TradingBar(market.MarketID, frequency, period)
	return err == nil && trading
}
