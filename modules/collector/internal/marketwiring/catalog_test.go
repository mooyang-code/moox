package marketwiring

import (
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/catalog"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/stretchr/testify/require"
)

// 单一来源的流水线从目录取数据源：登记过的（市场，品种，数据源）构造出对应类型、对应 SourceKey 的实例，没登记的组合报错。
func TestSingleSourceProvidersComeFromTheCatalog(t *testing.T) {
	for _, test := range []struct {
		market, instrument, provider string
		wantType, wantKey            string
	}{
		{"crypto", "spot", "binance", "*binance.MarketDataAdapter", "binance/spot_http"},
		{"crypto", "swap", "binance", "*binance.MarketDataAdapter", "binance/swap_http"},
		{"crypto", "equity", "binance", "", ""},
		{"crypto", "spot", "okx", "", ""},
		{"stockcn", "convertible_bond", "eastmoney", "*markethttp.Provider", "eastmoney/convertible_bond_http"},
		{"stockcn", "convertible_bond", "sina", "*markethttp.Provider", "sina/convertible_bond_http"},
		{"stockcn", "convertible_bond", "tencent", "", ""},
		{"stockcn", "index", "eastmoney", "*markethttp.Provider", "eastmoney/index_http"},
		{"stockcn", "index", "sina", "*markethttp.Provider", "sina/index_http"},
		{"stockcn", "index", "tencent", "*markethttp.Provider", "tencent/index_http"},
		{"stockcn", "index", "cni", "*markethttp.Provider", "cni/index_cni_http"},
		{"stockcn", "index", "sw", "*markethttp.Provider", "sw/index_sw_http"},
		{"stockcn", "index", "csindex", "*markethttp.Provider", "csindex/index_http"},
		{"crypto", "index", "sina", "", ""},
		{"stockcn", "futures", "x", "", ""},
	} {
		name := fmt.Sprintf("%s/%s/%s", test.market, test.instrument, test.provider)
		pipeline, err := newSingleSourceKlinePipeline(timerHandlerStorage{}, test.market, marketdata.InstrumentType(test.instrument), test.provider, nil)
		if test.wantType == "" {
			require.EqualError(t, err, fmt.Sprintf("行情源目录里没有 %s 的 %s 数据源 %q", test.market, test.instrument, test.provider), name)
			continue
		}
		require.NoError(t, err, name)
		provider, err := pipeline.Router.Provider(test.provider)
		require.NoError(t, err, name)
		require.Equal(t, test.wantType, fmt.Sprintf("%T", provider), name)
		require.Equal(t, test.wantKey, provider.Descriptor().SourceKey().String(), name)
	}
}

// 按路由分配的数据源从目录构造：source_id 写不写都得到路由登记的那一个。
func TestRoutedProvidersComeFromTheCatalog(t *testing.T) {
	route, found, err := marketfetch.RouteFor("stockcn", marketdata.InstrumentEquity)
	require.NoError(t, err)
	require.True(t, found)
	runtimes, err := marketfetch.LoadSourceRuntimes(route)
	require.NoError(t, err)
	sourceIDs := map[string]string{"sina": "stockcn_minute_http", "tencent": "stockcn_http", "eastmoney": "stockcn_http", "baidu": "stockcn_http", "tdx": "normal_7709"}
	for id, want := range map[string]string{
		"sina": "*sina.Provider sina/stockcn_minute_http", "tencent": "*tencent.Provider tencent/stockcn_http",
		"eastmoney": "*eastmoney.Provider eastmoney/stockcn_http", "baidu": "*baidu.Provider baidu/stockcn_http", "tdx": "*tdx.Provider tdx/normal_7709",
	} {
		for _, source := range []string{"", sourceIDs[id]} {
			provider, err := newRoutedProvider("stockcn", marketdata.InstrumentEquity, id, source, runtimes[id])
			require.NoError(t, err, id)
			require.Equal(t, want, fmt.Sprintf("%T %s", provider, provider.Descriptor().SourceKey()), id)
		}
	}
	_, err = newRoutedProvider("stockcn", marketdata.InstrumentEquity, "nope", "", marketfetch.SourceRuntime{})
	require.EqualError(t, err, `行情源目录里没有 stockcn 的 equity 数据源 "nope"`)
}

// 目录登记项的工厂构造出的实例与登记的 SourceKey 一致，能力只由实现的接口决定。
func TestCatalogEntriesBuildProvidersWithTheirOwnKeys(t *testing.T) {
	for _, entry := range catalog.Default().Entries() {
		provider, err := entry.Build(marketdata.Deps{})
		require.NoError(t, err, entry.Key.String())
		require.Equal(t, entry.Key, provider.Descriptor().SourceKey(), entry.Key.String())
	}
	providers, err := stream.ProvidersFrom(catalog.Default(), marketdata.Deps{})
	require.NoError(t, err)
	require.Len(t, providers, 1)
	require.Contains(t, providers, "binance")
}
