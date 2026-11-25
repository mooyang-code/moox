package marketwiring

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 建 K 线任务时的频率校验与取数装配同一口径：加密按币安声明的周期，A 股、港股、美股按路由文件里的频率。
func TestCheckKlineFrequencyFollowsCatalogAndRoutes(t *testing.T) {
	cases := []struct {
		market, marketType, provider string
		ok                           []string
		rejected                     []string
	}{
		{"crypto", "spot", "binance", []string{"1m", "5m", "15m", "30m", "1h", "1d", "1w", "1mo"}, []string{"4h"}},
		{"crypto", "swap", "binance", []string{"1m", "5m", "1d"}, []string{"4h"}},
		{"stockcn", "equity", "eastmoney", []string{"1m", "5m", "1d"}, []string{"15m", "1w"}},
		{"stockhk", "equity", "eastmoney", []string{"5m"}, []string{"1m", "1d"}},
		// 美股标签的 sync.source 是 eastmoney，K 线来源由路由文件分配（CNBC），不看标签来源。
		{"stockus", "equity", "eastmoney", []string{"5m"}, []string{"1m", "1d"}},
	}
	for _, item := range cases {
		for _, frequency := range item.ok {
			require.NoError(t, CheckKlineFrequency(item.market, item.marketType, item.provider, frequency), "%s/%s %s", item.market, item.marketType, frequency)
		}
		for _, frequency := range item.rejected {
			require.Error(t, CheckKlineFrequency(item.market, item.marketType, item.provider, frequency), "%s/%s %s", item.market, item.marketType, frequency)
		}
	}
	require.ErrorContains(t, CheckKlineFrequency("stockhk", "equity", "eastmoney", "1m"), "不支持 1m")
	require.ErrorContains(t, CheckKlineFrequency("crypto", "spot", "binance", "4h"), "不支持 4h")
	require.ErrorContains(t, CheckKlineFrequency("crypto", "spot", "okx", "1m"), "行情源目录里没有")
	// A 股的指数、可转债没有数据源：这类标签建任务时被拒。
	require.ErrorContains(t, CheckKlineFrequency("stockcn", "index", "eastmoney", "5m"), "行情源目录里没有")
	require.ErrorContains(t, CheckKlineFrequency("stockcn", "convertible_bond", "sina", "1d"), "行情源目录里没有")
}
