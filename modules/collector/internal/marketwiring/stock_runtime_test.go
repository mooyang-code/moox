package marketwiring

import (
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestNewStockCNProviderForSourceCarriesSourceIdentity(t *testing.T) {
	provider, err := newStockCNProvider("sina", "stockcn_minute_http", marketfetch.StockCNProviderRuntime{
		KlineSpec: marketfetch.StockCNProviderKlineConfig{MaxBarsPerRequest: 1023},
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1,
			Cooldown: time.Second, RequestTimeout: time.Second,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "sina", provider.Descriptor().ID)
	require.Equal(t, "stockcn_minute_http", provider.Descriptor().SourceID)
}

func TestNewStockCNProviderForSourceSupportsNormalTDX(t *testing.T) {
	provider, err := newStockCNProvider("tdx", "normal_7709", marketfetch.StockCNProviderRuntime{
		Hosts:     []string{"quotes.example"},
		Port:      7709,
		KlineSpec: marketfetch.StockCNProviderKlineConfig{MaxBarsPerRequest: 800},
	})
	require.NoError(t, err)
	require.Equal(t, "tdx", provider.Descriptor().ID)
	require.Equal(t, "normal_7709", provider.Descriptor().SourceID)
}

func TestNewStockKlinePipelineUsesTheFrequencyRoute(t *testing.T) {
	minute, err := NewStockKlinePipeline(timerHandlerStorage{}, "1m")
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, minute.CandidateChain)
	require.True(t, minute.AutoBindSource)
	require.Empty(t, minute.SourceID)

	five, err := NewStockKlinePipeline(timerHandlerStorage{}, "5m")
	require.NoError(t, err)
	require.Equal(t, []string{"eastmoney", "tdx"}, five.CandidateChain)

	_, err = NewStockKlinePipeline(timerHandlerStorage{}, "15m")
	require.ErrorContains(t, err, "没有 15m 的 K 线路由")
}

func TestNewMarketKlinePipelineCreatesBinanceSpotSource(t *testing.T) {
	pipeline, err := NewMarketKlinePipeline(timerHandlerStorage{}, "crypto", marketdata.InstrumentSpot, "binance", "spot_http")
	require.NoError(t, err)
	require.Equal(t, "crypto", pipeline.SpaceID)
	require.Equal(t, "crypto", pipeline.MarketID)
	require.Equal(t, "spot_http", pipeline.SourceID)
	require.Equal(t, []string{"binance"}, pipeline.CandidateChain)
}

func TestNewMarketKlinePipelineCreatesBinanceSwapSource(t *testing.T) {
	pipeline, err := NewMarketKlinePipeline(timerHandlerStorage{}, "crypto", marketdata.InstrumentSwap, "binance", "swap_http")
	require.NoError(t, err)
	require.Equal(t, marketdata.InstrumentSwap, pipeline.InstrumentType)
	require.Equal(t, marketdata.InstrumentSwap, pipeline.InstrumentType)
	require.Empty(t, pipeline.DatasetID)
	require.Equal(t, "swap_http", pipeline.SourceID)
	require.Equal(t, []string{"binance"}, pipeline.CandidateChain)
}
