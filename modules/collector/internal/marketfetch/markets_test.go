package marketfetch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	// 生产里标的规则随行情源目录登记（sources/catalog）；marketfetch 不引用目录，测试里直接引用两个市场的规则包。
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
	KlineRoutePrefixes: []string{stockCNKlineRoutePrefix, "binance_spot_kline_", "binance_swap_kline_"},
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

	route, err := loadStockCNRoute()
	require.NoError(t, err)
	providers, err := loadStockCNProviderRuntime(route)
	require.NoError(t, err)
	require.Contains(t, providers, "tdx")
	for _, source := range []string{"sina_hfq", "swsresearch", "sina_finance", "eastmoney_turnover", "eastmoney_select", "baidu"} {
		_, found, err := loadStockCNSourceRuntime(source)
		require.NoError(t, err, source)
		require.True(t, found, source)
	}
	for _, marketID := range []string{"crypto", "stockcn"} {
		_, err := markets.ConfigFor(marketID)
		require.NoError(t, err, marketID)
	}
}

// 环境变量仍然可以覆盖编进去的内容：指定了路由文件就读它。
func TestStockCNRoutePathOverridesEmbedded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route.yaml")
	require.NoError(t, os.WriteFile(path, []byte("market_id: stockcn\nklines:\n  - frequency: 5m\n    providers:\n      - {id: eastmoney, source_id: stockcn_http, weight: 1, role: primary}\n"), 0o600))
	t.Setenv("MOOX_STOCK_CN_ROUTE_PATH", path)
	route, err := loadStockCNRoute()
	require.NoError(t, err)
	require.Equal(t, []string{"eastmoney"}, route.ProviderIDs())
}
