package marketwiring

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestNewRoutedProviderCarriesSourceIdentity(t *testing.T) {
	provider, err := newRoutedProvider("stockcn", marketdata.InstrumentEquity, "sina", "stockcn_minute_http", marketfetch.SourceRuntime{
		KlineSpec: marketfetch.SourceKlineConfig{MaxBarsPerRequest: 1023},
		RateLimit: marketdata.RateLimitPolicy{
			RequestsPerSecond: 5, Burst: 2, MaxConcurrent: 1,
			Cooldown: time.Second, RequestTimeout: time.Second,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "sina", provider.Descriptor().ID)
	require.Equal(t, "stockcn_minute_http", provider.Descriptor().SourceID)
}

func TestNewRoutedProviderSupportsNormalTDX(t *testing.T) {
	provider, err := newRoutedProvider("stockcn", marketdata.InstrumentEquity, "tdx", "normal_7709", marketfetch.SourceRuntime{
		Hosts:     []string{"quotes.example"},
		Port:      7709,
		KlineSpec: marketfetch.SourceKlineConfig{MaxBarsPerRequest: 800},
	})
	require.NoError(t, err)
	require.Equal(t, "tdx", provider.Descriptor().ID)
	require.Equal(t, "normal_7709", provider.Descriptor().SourceID)
}

// 来源按路由分配的（市场，品种）：候选链按频率取路由，不看请求里的数据源；结算延迟取路由文件。
func TestNewKlinePipelineUsesTheFrequencyRoute(t *testing.T) {
	minute, err := NewKlinePipeline(timerHandlerStorage{}, "stockcn", marketdata.InstrumentEquity, "1m", "eastmoney", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, minute.CandidateChain)
	require.True(t, minute.AutoBindSource)
	require.Empty(t, minute.SourceID)
	require.Equal(t, 800*time.Millisecond, minute.SettleDelay)

	five, err := NewKlinePipeline(timerHandlerStorage{}, "stockcn", marketdata.InstrumentEquity, "5m", "", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"tdx", "eastmoney", "sina", "tencent"}, five.CandidateChain)

	_, err = NewKlinePipeline(timerHandlerStorage{}, "stockcn", marketdata.InstrumentEquity, "15m", "", nil)
	require.ErrorContains(t, err, "没有 15m 的 K 线路由")
}

// 单一来源的（市场，品种）：数据源是请求指定的那一个，source_id 取行情源目录登记的。
func TestNewKlinePipelineBindsTheSingleSource(t *testing.T) {
	for _, tc := range []struct {
		marketID   string
		instrument marketdata.InstrumentType
		provider   string
		sourceID   string
	}{
		{"crypto", marketdata.InstrumentSpot, "binance", "spot_http"},
		{"crypto", marketdata.InstrumentSwap, "binance", "swap_http"},
	} {
		pipeline, err := NewKlinePipeline(timerHandlerStorage{}, tc.marketID, tc.instrument, "1d", tc.provider, nil)
		require.NoError(t, err, "%+v", tc)
		require.Equal(t, tc.marketID, pipeline.SpaceID)
		require.Equal(t, tc.marketID, pipeline.MarketID)
		require.Equal(t, tc.instrument, pipeline.InstrumentType)
		require.Equal(t, tc.sourceID, pipeline.SourceID)
		require.Equal(t, []string{tc.provider}, pipeline.CandidateChain)
		require.False(t, pipeline.AutoBindSource)
		require.Zero(t, pipeline.SettleDelay)
		require.Empty(t, pipeline.DatasetID, "数据集取请求里的")
	}
}

// 合并两个构造函数后，币安的限速仍然来自请求带的限速：每秒请求数、突发、并发、超时都交给数据源，熔断阈值按并发算。
func TestNewKlinePipelineKeepsTheRequestLimitsForTheSingleSource(t *testing.T) {
	t.Setenv("MOOX_FETCH_FEED_CONCURRENCY", "")
	t.Setenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS", "")
	limits := &marketfetch.FeedLimits{RPS: 40, Burst: 60, FeedConcurrency: 16, RequestTimeoutMS: 3000}
	for _, instrument := range []marketdata.InstrumentType{marketdata.InstrumentSpot, marketdata.InstrumentSwap} {
		pipeline, err := NewKlinePipeline(timerHandlerStorage{}, "crypto", instrument, "1m", "binance", limits)
		require.NoError(t, err)
		spec, err := pipeline.Router.NewSession().KlineSpec("binance")
		require.NoError(t, err)
		require.Equal(t, 40.0, spec.RateLimit.RequestsPerSecond)
		require.Equal(t, 60, spec.RateLimit.Burst)
		require.Equal(t, 16, spec.RateLimit.MaxConcurrent)
		require.Equal(t, 3*time.Second, spec.RateLimit.RequestTimeout)
		require.Equal(t, marketfetch.KlineProviderAttemptBudget*16, pipeline.Router.BreakerThreshold())

		// 不带限速时数据源用自己的默认值，熔断阈值按进程环境（没设并发按 1）。
		plain, err := NewKlinePipeline(timerHandlerStorage{}, "crypto", instrument, "1m", "binance", nil)
		require.NoError(t, err)
		spec, err = plain.Router.NewSession().KlineSpec("binance")
		require.NoError(t, err)
		require.NotEqual(t, 40.0, spec.RateLimit.RequestsPerSecond)
		require.Equal(t, marketfetch.KlineProviderAttemptBudget, plain.Router.BreakerThreshold())
	}
}

func TestNewKlinePipelineRejectsSourcesMissingFromTheCatalog(t *testing.T) {
	for _, tc := range []struct {
		marketID   string
		instrument marketdata.InstrumentType
		provider   string
	}{
		{"crypto", marketdata.InstrumentSwap, "unknown"},
		{"crypto", marketdata.InstrumentSwap, ""},
		{"crypto", marketdata.InstrumentEquity, "binance"},
		{"crypto", marketdata.InstrumentIndex, "sina"},
		{"stockcn", marketdata.InstrumentConvertibleBond, "tencent"},
		{"stockcn", "futures", "eastmoney"},
		{"stockjp", marketdata.InstrumentEquity, "eastmoney"},
	} {
		_, err := NewKlinePipeline(timerHandlerStorage{}, tc.marketID, tc.instrument, "1d", tc.provider, nil)
		require.ErrorContains(t, err, "行情源目录里没有", "%+v", tc)
	}
	_, err := NewKlinePipeline(nil, "crypto", marketdata.InstrumentSpot, "1m", "binance", nil)
	require.ErrorContains(t, err, "缺少 Storage")
}

// 行情源目录里“按路由分配”的登记与各市场路由文件写的品种必须一致：装配看目录，请求来源、路由 ID、结算延迟校验看路由文件。
func TestCatalogRoutingMatchesTheRouteFiles(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range catalog.Default().Entries() {
		key := string(entry.MarketID) + "/" + string(entry.Instrument)
		if seen[key] {
			continue
		}
		seen[key] = true
		_, found, err := marketfetch.RouteFor(string(entry.MarketID), entry.Instrument)
		require.NoError(t, err, key)
		require.Equal(t, catalog.Default().Routed(entry.MarketID, entry.Instrument), found, key)
	}
	// 有路由文件的市场，路由管的品种在目录里必须有按路由分配的登记项。
	for _, marketID := range markets.IDs() {
		route, found, err := marketfetch.LoadRoute(marketID)
		require.NoError(t, err, marketID)
		if found {
			require.True(t, catalog.Default().Routed(marketdata.MarketID(marketID), route.Instrument()), marketID)
		}
	}
}

func rowFields(row *storagepb.RowFieldUpsert) map[string]*storagepb.TypedValue {
	fields := map[string]*storagepb.TypedValue{}
	for _, field := range row.GetFields() {
		fields[field.GetFieldId()] = field.GetValue()
	}
	return fields
}
