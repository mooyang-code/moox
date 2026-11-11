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

	swapPipeline, err := NewCryptoKlinePipeline(timerHandlerStorage{}, marketdata.InstrumentSwap, nil)
	require.NoError(t, err)
	source, ok := marketdata.FindFieldSource(swapPipeline.Fields, "funding_rate")
	require.True(t, ok)
	require.Same(t, swap, source.(interface {
		FundingCache() *marketfetch.FundingCache
	}).FundingCache(), "拉取管线的资金费率来源用的是同一份缓存")
	require.Equal(t, []string{"funding_rate"}, swapPipeline.ExtraFieldIDs)
	spotPipeline, err := NewCryptoKlinePipeline(timerHandlerStorage{}, marketdata.InstrumentSpot, nil)
	require.NoError(t, err)
	require.Empty(t, spotPipeline.Fields)
	require.Equal(t, []string{"funding_rate"}, spotPipeline.ExtraFieldIDs, "现货管线没有来源，选了资金费率按配置错误处理")
}

// A 股股票目前没有附加字段来源；管线照常构造。
func TestStockPipelineHasNoFieldSourcesYet(t *testing.T) {
	sources, err := FieldSources("stockcn", marketdata.InstrumentEquity)
	require.NoError(t, err)
	require.Empty(t, sources)
}
