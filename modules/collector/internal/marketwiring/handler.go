// Package marketwiring wires concrete data sources into common collection
// pipelines. Neither the scheduler nor marketfetch imports provider adapters.
package marketwiring

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
)

func NewHandler() *marketfetch.Handler {
	h := marketfetch.NewHandler()
	h.NewMarketKlinePipeline = NewMarketKlinePipeline
	h.NewCryptoKlinePipeline = NewCryptoKlinePipeline
	h.NewStockKlinePipeline = NewStockKlinePipeline
	h.ResolveSourceID = DefaultSourceID
	h.ResolveSymbol = ResolveSymbol
	h.PlanPeriodValid = stockCNPlanPeriodValid()
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

// stockCNPlanPeriodValid 返回采集计划的周期过滤：stockcn 只在交易日历的交易分钟执行，其余市场总是执行。
// 日历加载失败时关闭执行（返回 false），避免在非交易时段制造无意义的提交。
func stockCNPlanPeriodValid() func(marketfetch.PlanMarket, string, time.Time) bool {
	var once sync.Once
	var calendar *stockmarket.Calendar
	var loadErr error
	return func(market marketfetch.PlanMarket, frequency string, period time.Time) bool {
		if !strings.EqualFold(market.MarketID, marketfetch.StockCNSpaceID) {
			return true
		}
		once.Do(func() { calendar, loadErr = loadStockCNCalendar() })
		if loadErr != nil || calendar == nil || calendar.Location() == nil || !strings.EqualFold(frequency, "1m") {
			return false
		}
		bars, err := calendar.ExpectedMinuteBars(period.In(calendar.Location()).Format("2006-01-02"))
		if err != nil {
			return false
		}
		for _, bar := range bars {
			if bar.Equal(period.UTC()) {
				return true
			}
		}
		return false
	}
}
