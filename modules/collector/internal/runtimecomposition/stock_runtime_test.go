package runtimecomposition

import (
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestNewStockCNProviderForSourceCarriesSourceIdentity(t *testing.T) {
	provider, err := newStockCNProviderForSource("sina", "stockcn_minute_http", marketfetch.StockCNProviderRuntime{
		KlineSpec: marketfetch.StockCNProviderKlineConfig{Frequency: "1m", MaxBarsPerRequest: 1023},
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
	provider, err := newStockCNProviderForSource("tdx", "normal_7709", marketfetch.StockCNProviderRuntime{
		Hosts: []string{"quotes.example"},
		Port:  7709,
		KlineSpec: marketfetch.StockCNProviderKlineConfig{
			Frequency:         "1m",
			MaxBarsPerRequest: 800,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "tdx", provider.Descriptor().ID)
	require.Equal(t, "normal_7709", provider.Descriptor().SourceID)
}

func TestNewStockKlinePipelineUsesSourceBoundEnvironment(t *testing.T) {
	t.Setenv("MOOX_MARKET_FETCH_PROVIDER", "tencent")
	t.Setenv("MOOX_MARKET_FETCH_SOURCE_ID", "stockcn_http")
	pipeline, err := NewStockKlinePipeline(timerHandlerStorage{})
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, pipeline.CandidateChain)
	require.Empty(t, pipeline.SourceID)
}

func TestNewMarketKlinePipelineCreatesCatalogGatedHongKongSource(t *testing.T) {
	pipeline, err := NewMarketKlinePipeline(timerHandlerStorage{}, "stockhk", marketdata.InstrumentEquity, "eastmoney", "stockhk_http")
	require.NoError(t, err)
	require.Equal(t, "stockhk", pipeline.SpaceID)
	require.Equal(t, "stockhk", pipeline.MarketID)
	require.Equal(t, "stockhk_http", pipeline.SourceID)
	require.Equal(t, []string{"eastmoney"}, pipeline.CandidateChain)
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
	require.Equal(t, marketdata.ProductSwap, pipeline.ProductType)
	require.Equal(t, marketdata.InstrumentSwap, pipeline.InstrumentType)
	require.Empty(t, pipeline.DatasetID)
	require.Equal(t, "swap_http", pipeline.SourceID)
	require.Equal(t, []string{"binance"}, pipeline.CandidateChain)
}
