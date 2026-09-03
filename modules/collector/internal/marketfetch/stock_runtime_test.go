package marketfetch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestLoadStockCNRouteUsesConfiguredProviderStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`market_id: stockcn
route_id: stockcn_equity_kline_1m_v4
frequency: 1m
providers:
  - id: eastmoney
    weight: 3
    kline: active
    instrument: active
  - id: sina
    weight: 2
    kline: active
    instrument: active
  - id: tencent
    weight: 1
    kline: active
    instrument: disabled
  - id: baidu
    weight: 1
    kline: shadow
    instrument: active
`), 0o600))

	route, err := loadStockCNRouteFile(path)
	require.NoError(t, err)
	require.Equal(t, []string{"eastmoney", "sina", "tencent"}, route.KlineProviders())
	require.Equal(t, []string{"eastmoney", "sina", "baidu"}, route.InstrumentProviders())
	require.Equal(t, map[string]int{"sina": 2, "tencent": 1, "eastmoney": 3}, route.KlineWeights())
}

func TestLoadStockCNProviderRuntimeUsesPackagedFeedPolicies(t *testing.T) {
	route, err := loadStockCNRouteFile(filepath.Join("..", "..", "config", "markets", "stockcn", "route.yaml"))
	require.NoError(t, err)
	t.Setenv("MOOX_STOCK_CN_SOURCE_CONFIG_DIR", filepath.Join("..", "..", "configs", "scf", "stockcn", "sources", "market"))
	providers, err := loadStockCNProviderRuntime(route)
	require.NoError(t, err)

	require.True(t, providers["sina"].KlineEnabled)
	require.True(t, providers["sina"].InstrumentEnabled)
	require.Equal(t, 1023, providers["sina"].KlineSpec.MaxBarsPerRequest)
	require.Equal(t, 5.0, providers["sina"].RateLimit.RequestsPerSecond)
	require.Equal(t, 1, providers["sina"].RateLimit.MaxConcurrent)
	// The source file advertises Baidu's instrument protocol, while the
	// release route intentionally keeps it disabled. EastMoney and Sina are
	// the active complete-snapshot sources in the release route.
	require.True(t, providers["baidu"].InstrumentEnabled)
	require.Equal(t, []string{"sina", "eastmoney"}, route.InstrumentProviders())
	require.NotContains(t, route.InstrumentProviders(), "baidu")
	require.True(t, providers["baidu"].KlineShadow)
}

func TestStockCNRoutePutsTDXBeforeEastMoneyAsFallbacks(t *testing.T) {
	route, err := loadStockCNRouteFile(filepath.Join("..", "..", "config", "markets", "stockcn", "route.yaml"))
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, route.KlineProviders())
	require.Equal(t, []string{"sina", "tencent"}, route.KlinePrimaryProviders())
}

func TestNewStockCNProviderForSourceCarriesSourceIdentity(t *testing.T) {
	provider, err := newStockCNProviderForSource("sina", "stockcn_minute_http", stockCNProviderRuntime{
		KlineSpec: stockCNProviderKlineFile{Frequency: "1m", MaxBarsPerRequest: 1023},
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
	provider, err := newStockCNProviderForSource("tdx", "normal_7709", stockCNProviderRuntime{
		Hosts: []string{"quotes.example"},
		Port:  7709,
		KlineSpec: stockCNProviderKlineFile{
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

func TestStockCNRouteRequiresThreeActiveKlineProviders(t *testing.T) {
	route := stockCNRoute{
		MarketID:  "stockcn",
		RouteID:   StockCNRouteID,
		Frequency: "1m",
		Providers: []stockCNRouteProvider{
			{ID: "sina", Weight: 1, Kline: "active", Instrument: "active"},
			{ID: "tencent", Weight: 1, Kline: "active", Instrument: "disabled"},
			{ID: "eastmoney", Weight: 1, Kline: "disabled", Instrument: "active"},
		},
	}

	require.ErrorContains(t, route.Validate(), "at least three active stockcn kline providers")
}

func TestStockCNRouteRejectsHistoryBeyondProviderPageCapability(t *testing.T) {
	route := stockCNRoute{
		MarketID:  StockCNSpaceID,
		RouteID:   StockCNRouteID,
		Frequency: "1m",
		History:   stockCNHistoryFile{MaxLookback: "2d"},
		Providers: []stockCNRouteProvider{
			{ID: "eastmoney", Weight: 1, Kline: "active", Instrument: "active"},
			{ID: "sina", Weight: 1, Kline: "active", Instrument: "active"},
			{ID: "tencent", Weight: 1, Kline: "active", Instrument: "disabled"},
		},
	}

	require.ErrorContains(t, route.Validate(), "history.max_lookback")
}
