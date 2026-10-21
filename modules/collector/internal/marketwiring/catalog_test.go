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

// 改为从目录取之前三处 switch 的每个分支：同样的组合构造出同类型、同 SourceKey 的数据源，未登记的组合报同样的错误。
func TestMarketProvidersFromCatalogMatchTheFormerSwitch(t *testing.T) {
	for _, test := range []struct {
		market, instrument, provider, source string
		wantType, wantKey, wantErr           string
	}{
		{"crypto", "spot", "binance", "spot_http", "*binance.MarketDataAdapter", "binance/spot_http", ""},
		{"crypto", "swap", "binance", "swap_http", "*binance.MarketDataAdapter", "binance/swap_http", ""},
		{"crypto", "spot", "binance", "swap_http", "", "", "unsupported crypto spot source binance/swap_http"},
		{"crypto", "equity", "binance", "x", "", "", "unsupported crypto instrument source binance/x"},
		{"crypto", "spot", "okx", "spot_http", "", "", `unsupported market "crypto"`},
		{"stockcn", "convertible_bond", "eastmoney", "convertible_bond_http", "*markethttp.Provider", "eastmoney/convertible_bond_http", ""},
		{"stockcn", "convertible_bond", "sina", "convertible_bond_http", "*markethttp.Provider", "sina/convertible_bond_http", ""},
		{"stockcn", "convertible_bond", "tencent", "convertible_bond_http", "", "", "unsupported convertible_bond source tencent/convertible_bond_http"},
		{"stockcn", "index", "eastmoney", "index_http", "*markethttp.Provider", "eastmoney/index_http", ""},
		{"stockcn", "index", "sina", "index_http", "*markethttp.Provider", "sina/index_http", ""},
		{"stockcn", "index", "tencent", "index_http", "*markethttp.Provider", "tencent/index_http", ""},
		{"stockcn", "index", "cni", "index_cni_http", "*markethttp.Provider", "cni/index_cni_http", ""},
		{"stockcn", "index", "sw", "index_sw_http", "*markethttp.Provider", "sw/index_sw_http", ""},
		{"stockcn", "index", "csindex", "index_http", "*markethttp.Provider", "csindex/index_http", ""},
		{"stockcn", "index", "csindex", "bad", "", "", "unsupported index source csindex/bad"},
		{"crypto", "index", "sina", "index_http", "", "", `index market "crypto" is unsupported`},
		{"stockcn", "futures", "x", "y", "", "", `unsupported market "stockcn"`},
		// A 股股票不走这条路（由 A 股流水线按路由构造）。
		{"stockcn", "equity", "sina", "stockcn_minute_http", "", "", `unsupported market "stockcn"`},
	} {
		name := fmt.Sprintf("%s/%s/%s/%s", test.market, test.instrument, test.provider, test.source)
		provider, err := newMarketProvider(test.market, marketdata.InstrumentType(test.instrument), test.provider, test.source)
		if test.wantErr != "" {
			require.EqualError(t, err, test.wantErr, name)
			continue
		}
		require.NoError(t, err, name)
		require.Equal(t, test.wantType, fmt.Sprintf("%T", provider), name)
		require.Equal(t, test.wantKey, provider.Descriptor().SourceKey().String(), name)
	}
}

func TestStockCNProvidersFromCatalogMatchTheFormerSwitch(t *testing.T) {
	route, err := marketfetch.LoadStockCNRoute()
	require.NoError(t, err)
	configs, err := marketfetch.LoadStockCNProviderRuntime(route)
	require.NoError(t, err)
	for id, want := range map[string]string{
		"sina": "*sina.Provider sina/stockcn_minute_http", "tencent": "*tencent.Provider tencent/stockcn_http",
		"eastmoney": "*eastmoney.Provider eastmoney/stockcn_http", "baidu": "*baidu.Provider baidu/stockcn_http", "tdx": "*tdx.Provider tdx/normal_7709",
	} {
		for _, source := range []string{"", stockCNSourceID(route, id)} {
			provider, err := newStockCNProvider(id, source, configs[id])
			require.NoError(t, err, id)
			require.Equal(t, want, fmt.Sprintf("%T %s", provider, provider.Descriptor().SourceKey()), id)
		}
	}
	_, err = newStockCNProvider("nope", "", marketfetch.StockCNProviderRuntime{})
	require.EqualError(t, err, `unsupported stockcn provider "nope"`)
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
