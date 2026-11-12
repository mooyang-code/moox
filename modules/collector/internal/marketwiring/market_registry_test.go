package marketwiring

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/stretchr/testify/require"
)

// 默认 source_id 查行情源目录：现有任务用到的取值不变。
func TestDefaultSourceIDComesFromCatalog(t *testing.T) {
	for _, tc := range []struct{ market, provider, instrument, want string }{
		{"crypto", "binance", "spot", "spot_http"},
		{"crypto", "binance", "swap", "swap_http"},
		{"stockcn", "sina", "equity", "stockcn_minute_http"},
		{"stockcn", "eastmoney", "equity", "stockcn_http"},
		{"stockcn", "tencent", "equity", "stockcn_http"},
		{"stockcn", "baidu", "equity", "stockcn_http"},
		// 目录里登记的是 index_http；原来写死的表给的是股票的 stockcn_http。
		{"stockcn", "eastmoney", "index", "index_http"},
		{"stockcn", "sina", "convertible_bond", "convertible_bond_http"},
		// 同一个数据源在别的市场没有登记。
		{"crypto", "eastmoney", "equity", ""},
		{"stockhk", "eastmoney", "equity", ""},
		{"stockcn", "unknown", "equity", ""},
	} {
		require.Equal(t, tc.want, DefaultSourceID(tc.market, tc.provider, tc.instrument), "%s/%s/%s", tc.market, tc.provider, tc.instrument)
	}
}

func TestResolveSymbolUsesSubjectRules(t *testing.T) {
	got, err := ResolveSymbol("eastmoney", "stockcn", "equity", "600000.XSHG")
	require.NoError(t, err)
	require.Equal(t, "sh600000", got)
	// A 股股票的来源按路由分配，不看标签的数据源。
	got, err = ResolveSymbol("manual", "stockcn", "equity", "000001.XSHE")
	require.NoError(t, err)
	require.Equal(t, "sz000001", got)

	_, err = ResolveSymbol("binance", "crypto", "swap", "BTC-USDT-SPOT")
	require.Error(t, err, "后缀与品种不符")
	_, err = ResolveSymbol("binance", "stockus", "equity", "AAPL.XNAS")
	require.ErrorIs(t, err, markets.ErrUnknownMarket)
}

// 指标标签的允许取值来自已登记市场、行情源目录和路由；现有路由 ID 都在里面，通达信也在数据源里。
func TestFeedLabelsCoverExistingValues(t *testing.T) {
	labels := FeedLabels()
	require.Equal(t, []string{"crypto", "stockcn", "stockhk"}, labels.Markets)
	for _, provider := range []string{"binance", "sina", "tencent", "eastmoney", "baidu", "tdx"} {
		require.Contains(t, labels.Providers, provider)
	}
	for _, route := range []string{"stockcn_instrument_v1", "binance_spot_instrument_v1", "binance_swap_instrument_v1"} {
		require.Contains(t, labels.Routes, route)
	}
	for _, prefix := range []string{"stockcn_equity_kline_", "binance_spot_kline_", "binance_swap_kline_"} {
		require.Contains(t, labels.KlineRoutePrefixes, prefix)
	}
}

// 两张表的键都带市场；指数成分表不依赖全部标的表；A 股只启用路由里列为标的来源的数据源。
func TestSubjectListersAreKeyedByMarket(t *testing.T) {
	listers, indexes, err := NewSubjectListers()
	require.NoError(t, err)
	keys := make([]subjectsync.ListerKey, 0, len(listers))
	for key := range listers {
		keys = append(keys, key)
	}
	require.ElementsMatch(t, []subjectsync.ListerKey{
		{MarketID: "crypto", Source: "binance", InstrumentType: "spot"},
		{MarketID: "crypto", Source: "binance", InstrumentType: "swap"},
		{MarketID: "stockcn", Source: "sina", InstrumentType: "equity"},
		{MarketID: "stockcn", Source: "eastmoney", InstrumentType: "equity"},
	}, keys)
	require.Len(t, indexes, 1)
	require.Contains(t, indexes, subjectsync.IndexListerKey{MarketID: "stockcn", Source: "eastmoney", Index: "000300"})
}

// 环境变量指向不存在的目录或文件时（旧发布流程在 collector.env 里写过），回落到编进二进制的内容：
// lister 仍是 4 个加 1 个指数，候选链、每函数并发推导都与内置配置一致。
func TestMissingOverrideFallsBackToEmbeddedFiles(t *testing.T) {
	wantPipeline, err := NewStockKlinePipeline(timerHandlerStorage{}, "5m")
	require.NoError(t, err)
	sample := marketfetch.TaskGroup{Frequency: "5m"}
	wantFetch := marketfetch.StockCNFetchLimits()(sample, marketfetch.PlanFetch{})
	wantSize := marketfetch.StockCNAutoSize(marketfetch.FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 8})(sample, 300)

	missing := filepath.Join(t.TempDir(), "no_such_dir")
	t.Setenv("MOOX_STOCK_CN_SOURCE_CONFIG_DIR", missing)
	t.Setenv("MOOX_STOCK_CN_ROUTE_PATH", filepath.Join(missing, "route.yaml"))

	listers, indexes, err := NewSubjectListers()
	require.NoError(t, err)
	require.Len(t, listers, 4)
	require.Len(t, indexes, 1)

	pipeline, err := NewStockKlinePipeline(timerHandlerStorage{}, "5m")
	require.NoError(t, err)
	require.Equal(t, wantPipeline.CandidateChain, pipeline.CandidateChain)
	require.Equal(t, []string{"tdx", "eastmoney", "sina", "tencent"}, pipeline.CandidateChain)
	require.Equal(t, wantFetch, marketfetch.StockCNFetchLimits()(sample, marketfetch.PlanFetch{}))
	require.NotZero(t, wantFetch.Concurrency)
	require.Equal(t, wantSize, marketfetch.StockCNAutoSize(marketfetch.FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 8})(sample, 300))
	require.NotZero(t, wantSize)
}
