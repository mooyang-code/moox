package marketwiring

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// K 线回看按行情源目录的声明：按路由分配的取候选链上的最大值，有来源支持任意区间就不限。
func TestKlineLookbackFollowsCatalogAndRoutes(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		market   marketfetch.PlanMarket
		freq     string
		lookback time.Duration
		bounded  bool
	}{
		// 港股 5m：东方财富 28 天、腾讯只有当天，取 28 天。来源按路由分配，不看任务标签的来源（港股标签的来源是腾讯）。
		{marketfetch.PlanMarket{MarketID: "stockhk", InstrumentType: "equity", MarketType: "equity", Provider: "tencent", SourceID: "tencent"}, "5m", 28 * day, true},
		// A 股 5m：通达信 365 天、东方财富 30 天、新浪 20 天、腾讯 5 天，取 365 天。
		{marketfetch.PlanMarket{MarketID: "stockcn", InstrumentType: "equity", MarketType: "equity"}, "5m", 365 * day, true},
		// A 股 1d：东方财富日线按日期区间取、声明任意区间，不限（通达信只有 10 年）。
		{marketfetch.PlanMarket{MarketID: "stockcn", InstrumentType: "equity", MarketType: "equity"}, "1d", 0, false},
		// 美股 CNBC、币安支持任意区间。
		{marketfetch.PlanMarket{MarketID: "stockus", InstrumentType: "equity", MarketType: "equity"}, "5m", 0, false},
		{marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", MarketType: "spot", Provider: "binance"}, "1m", 0, false},
		{marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "swap", MarketType: "swap", Provider: "binance", SourceID: "swap_http"}, "1m", 0, false},
	}
	for _, item := range cases {
		lookback, bounded, err := KlineLookback(item.market, item.freq)
		require.NoError(t, err, "%s %s", item.market.MarketID, item.freq)
		require.Equal(t, item.bounded, bounded, "%s %s", item.market.MarketID, item.freq)
		require.Equal(t, item.lookback, lookback, "%s %s", item.market.MarketID, item.freq)
	}
	_, _, err := KlineLookback(marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "okx"}, "1m")
	require.ErrorContains(t, err, "行情源目录里没有")
}
