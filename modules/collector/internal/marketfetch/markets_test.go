package marketfetch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/prometheus/client_golang/prometheus"

	// 生产里标的规则由装配入口（marketwiring）引用后登记；marketfetch 不引用装配层，测试里直接引用两个市场的规则包。
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/crypto"
	_ "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// testFeedLabels 是测试里 feed 指标标签允许的取值，与 marketwiring.FeedLabels 对现有两个市场给出的一致。
var testFeedLabels = FeedLabels{
	Markets:            []string{"crypto", "stockcn"},
	Providers:          []string{"binance", "sina", "tencent", "eastmoney", "baidu", "tdx"},
	Routes:             []string{"stockcn_instrument_v1", "binance_spot_instrument_v1", "binance_swap_instrument_v1"},
	KlineRoutePrefixes: []string{"stockcn_equity_kline_", "binance_spot_kline_", "binance_swap_kline_"},
}

// StockCNSpaceID 是测试里用的 A 股市场 ID。
const StockCNSpaceID = "stockcn"

// StockCNRouteID 是测试里 A 股股票某个频率的路由 ID。
func StockCNRouteID(frequency string) string {
	return KlineRouteID(StockCNSpaceID, marketdata.InstrumentEquity, frequency)
}

// stockKlineRow 是测试里构造 A 股股票行的简写。
func stockKlineRow(bar marketdata.NormalizedKline, datasetID, routeID string, routeRank int, instrumentName string) (*storagepb.RowFieldUpsert, error) {
	return equityKlineRow(bar, equityRowKey{SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID, DatasetID: datasetID, SeriesTag: "default"}, routeID, routeRank, instrumentName)
}

// 市场数据文件编在二进制里：换到一个没有任何配置文件的目录，路由、数据源运行参数、market.yaml 仍然读得到。
func TestMarketFilesAreEmbedded(t *testing.T) {
	t.Chdir(t.TempDir())
	_, err := os.Stat(filepath.Join("config", "markets"))
	require.True(t, os.IsNotExist(err))

	route, found, err := LoadRoute(StockCNSpaceID)
	require.NoError(t, err)
	require.True(t, found)
	providers, err := LoadSourceRuntimes(route)
	require.NoError(t, err)
	require.Contains(t, providers, "tdx")
	for _, source := range []string{"sina_hfq", "swsresearch", "sina_finance", "eastmoney_turnover", "eastmoney_select", "baidu"} {
		_, found, err := LoadSourceRuntime(StockCNSpaceID, source)
		require.NoError(t, err, source)
		require.True(t, found, source)
	}
	for _, marketID := range []string{"crypto", "stockcn"} {
		_, err := markets.ConfigFor(marketID)
		require.NoError(t, err, marketID)
	}
}

// 环境变量仍然可以覆盖编进去的内容：目录里有这个市场的路由文件就读它；没有路由文件的市场不受影响。
func TestMarketConfigDirOverridesEmbedded(t *testing.T) {
	writeMarketFile(t, t.TempDir(), "stockcn/route.yaml", "market_id: stockcn\ninstrument_type: equity\nsettle_delay_ms: 800\nklines:\n  - frequency: 5m\n    providers:\n      - {id: eastmoney, source_id: stockcn_http, weight: 1, role: primary}\n")
	route, found, err := LoadRoute(StockCNSpaceID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []string{"eastmoney"}, route.ProviderIDs())
	_, found, err = LoadRoute("crypto")
	require.NoError(t, err)
	require.False(t, found, "crypto 没有路由文件")
}

// 构造指标时必须给出标签的允许取值：漏了在启动期就 panic，不会把所有标签悄悄记成 unknown。
func TestNewMetricsRequiresFeedLabels(t *testing.T) {
	require.Panics(t, func() { NewMetrics(prometheus.NewRegistry(), FeedLabels{}) })
}

// 环境变量指向的目录里没有这个数据源的文件时，回落到编进二进制的内容；两处都没有才算没配置。
func TestSourceConfigDirFallsBackPerFile(t *testing.T) {
	t.Setenv(MarketConfigDirEnv, filepath.Join(t.TempDir(), "no_such_dir"))
	config, found, err := LoadSourceRuntime(StockCNSpaceID, "sina")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1023, config.KlineSpec.MaxBarsPerRequest)
	_, found, err = LoadSourceRuntime(StockCNSpaceID, "no_such_source")
	require.NoError(t, err)
	require.False(t, found)
}
