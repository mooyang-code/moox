package marketwiring

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 资金费率是行情源的可选能力：币安永续有，现货与 A 股没有；同一行情源在进程内只有一份缓存，推送与拉取共用。
func TestFundingCapabilityComesFromTheSourceCatalog(t *testing.T) {
	require.True(t, SupportsFundingRate("crypto", "binance", "swap"))
	require.False(t, SupportsFundingRate("crypto", "binance", "spot"))
	require.False(t, SupportsFundingRate("stockcn", "eastmoney", "equity"))

	swap := FundingCacheFor("crypto", marketdata.InstrumentSwap, "binance", "swap_http")
	require.NotNil(t, swap)
	require.Same(t, swap, FundingCacheForMarket(marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "swap", Provider: "binance", SourceID: "swap_http", MarketType: "swap"}))
	require.Same(t, swap, FundingCacheFor("crypto", marketdata.InstrumentSwap, "binance", ""), "没写 source_id 时按默认来源")
	require.Nil(t, FundingCacheFor("crypto", marketdata.InstrumentSpot, "binance", "spot_http"))

	swapPipeline, err := NewCryptoKlinePipeline(timerHandlerStorage{}, marketdata.InstrumentSwap, nil)
	require.NoError(t, err)
	require.Same(t, swap, swapPipeline.Funding)
	spotPipeline, err := NewCryptoKlinePipeline(timerHandlerStorage{}, marketdata.InstrumentSpot, nil)
	require.NoError(t, err)
	require.Nil(t, spotPipeline.Funding)
}
