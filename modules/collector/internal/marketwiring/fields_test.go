package marketwiring

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 资金费率是行情源目录里的附加字段：币安永续有来源，现货与 A 股没有；资金费率缓存在进程内只有一份，推送与拉取共用。
func TestFundingRateFieldComesFromTheSourceCatalog(t *testing.T) {
	offers := FieldOffers()
	offer, ok := offers.Find("crypto", "swap", "funding_rate")
	require.True(t, ok)
	require.Equal(t, "binance", offer.ProviderID)
	require.Equal(t, []string{"1h", "1d"}, offer.Spec.Frequencies)
	_, ok = offers.Find("crypto", "spot", "funding_rate")
	require.False(t, ok, "现货实例不提供资金费率")
	_, ok = offers.Find("stockcn", "equity", "funding_rate")
	require.False(t, ok)

	swapMarket := marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "swap", Provider: "binance", SourceID: "swap_http", MarketType: "swap"}
	swap := FundingCacheForMarket(swapMarket)
	require.NotNil(t, swap)
	swapMarket.SourceID = ""
	require.Same(t, swap, FundingCacheForMarket(swapMarket), "没写 source_id 时按默认来源")
	require.Nil(t, FundingCacheForMarket(marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"}))

	swapPipeline, err := NewKlinePipeline(timerHandlerStorage{}, "crypto", marketdata.InstrumentSwap, "1m", "binance", nil)
	require.NoError(t, err)
	source, ok := marketdata.FindFieldSource(swapPipeline.Fields, "funding_rate")
	require.True(t, ok)
	require.Same(t, swap, source.(interface {
		FundingCache() *marketfetch.FundingCache
	}).FundingCache(), "拉取管线的资金费率来源用的是同一份缓存")
	require.Equal(t, []string{"funding_rate"}, swapPipeline.ExtraFieldIDs)
	spotPipeline, err := NewKlinePipeline(timerHandlerStorage{}, "crypto", marketdata.InstrumentSpot, "1m", "binance", nil)
	require.NoError(t, err)
	require.Empty(t, spotPipeline.Fields)
	require.Equal(t, []string{"funding_rate"}, spotPipeline.ExtraFieldIDs, "现货管线没有来源，选了资金费率按配置错误处理")
}

// A 股股票的附加字段来自五个只提供字段的来源（复权、行业、财务、换手率、股本），全部只适用于日线；它们的运行参数能从数据源配置文件加载。
func TestStockFieldSourcesComeFromTheSourceCatalog(t *testing.T) {
	sources, err := FieldSources("stockcn", marketdata.InstrumentEquity)
	require.NoError(t, err)
	providers := make([]string, 0, len(sources))
	for _, source := range sources {
		providers = append(providers, source.Descriptor().ID)
		_, found, err := marketfetch.LoadSourceRuntime("stockcn", source.Descriptor().ID)
		require.NoError(t, err)
		require.True(t, found, "%s 要有数据源配置文件", source.Descriptor().ID)
	}
	require.Equal(t, []string{"sina_hfq", "swsresearch", "sina_finance", "eastmoney_turnover", "eastmoney_select"}, providers)

	offers := FieldOffers()
	specs := offers.Specs("stockcn")
	require.Len(t, specs, 27)
	for _, spec := range specs {
		require.Equal(t, []string{"1d"}, spec.Frequencies, spec.ID)
		// 换手率在停牌日补出的行上是 0，其余字段沿用前值。
		want := "prev"
		if spec.ID == "turnover_rate" {
			want = "zero"
		}
		require.Equal(t, want, spec.Fill, spec.ID)
	}
	for id, provider := range map[string]string{
		"adj_factor": "sina_hfq", "sw_l1": "swsresearch", "sw_l3": "swsresearch", "fin_report_period": "sina_finance", "fin_net_profit_parent_ttm": "sina_finance",
		"turnover_rate": "eastmoney_turnover", "total_shares": "eastmoney_select", "float_shares": "eastmoney_select", "dividend_per_share": "eastmoney_select", "is_st": "eastmoney_select",
	} {
		offer, ok := offers.Find("stockcn", "equity", id)
		require.True(t, ok, id)
		require.Equal(t, provider, offer.ProviderID, id)
		_, ok = offers.Find("stockcn", "index", id)
		require.False(t, ok, "指数没有 %s", id)
		_, ok = offers.Find("crypto", "swap", id)
		require.False(t, ok)
	}
}
