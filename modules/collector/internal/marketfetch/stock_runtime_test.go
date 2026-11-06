package marketfetch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadStockCNRouteSplitsKlineRoutesByFrequency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`market_id: stockcn
instrument_providers: [eastmoney, sina]
klines:
  - frequency: 1m
    providers:
      - {id: sina, source_id: stockcn_minute_http, weight: 2, role: primary}
      - {id: eastmoney, source_id: stockcn_http, weight: 3, role: primary}
      - {id: tdx, source_id: normal_7709, weight: 1, role: fallback}
  - frequency: 5m
    providers:
      - {id: eastmoney, source_id: stockcn_http, weight: 1, role: primary}
      - {id: tdx, source_id: normal_7709, weight: 1, role: fallback}
`), 0o600))

	route, err := loadStockCNRouteFile(path)
	require.NoError(t, err)
	require.Equal(t, []string{"eastmoney", "sina"}, route.InstrumentProviders())
	require.Equal(t, []string{"eastmoney", "sina", "tdx"}, route.ProviderIDs())

	minute, err := route.Kline("1m")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_1m", minute.RouteID())
	require.Equal(t, []string{"sina", "eastmoney", "tdx"}, minute.ProviderChain())
	require.Equal(t, []stockCNSource{{Provider: "eastmoney", SourceID: "stockcn_http", Weight: 3}, {Provider: "sina", SourceID: "stockcn_minute_http", Weight: 2}}, minute.PrimarySources())

	five, err := route.Kline("5m")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_5m", five.RouteID())
	require.Equal(t, []string{"eastmoney", "tdx"}, five.ProviderChain())
	require.Equal(t, []stockCNSource{{Provider: "eastmoney", SourceID: "stockcn_http", Weight: 1}}, five.PrimarySources())

	_, err = route.Kline("15m")
	require.ErrorContains(t, err, "没有 15m 的 K 线路由")
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
	require.Equal(t, 50.0, providers["sina"].RateLimit.RequestsPerSecond)
	require.Equal(t, 16, providers["sina"].RateLimit.MaxConcurrent)
	require.Equal(t, []string{"sina", "eastmoney"}, route.InstrumentProviders())
	require.Contains(t, providers["eastmoney"].Hosts, "push2his.eastmoney.com", "5m K 线走 push2his")
}

func TestReleaseStockCNRouteWeights5mTowardsTdxWithTencentAsFallback(t *testing.T) {
	route, err := loadStockCNRouteFile(filepath.Join("..", "..", "config", "markets", "stockcn", "route.yaml"))
	require.NoError(t, err)
	minute, err := route.Kline("1m")
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, minute.ProviderChain())
	five, err := route.Kline("5m")
	require.NoError(t, err)
	require.Equal(t, []string{"tdx", "eastmoney", "sina", "tencent"}, five.ProviderChain())
	// PrimarySources 按来源名排序，与候选链的回落顺序无关；分配只看权重。
	require.Equal(t, []stockCNSource{{Provider: "eastmoney", SourceID: "stockcn_http", Weight: 2}, {Provider: "sina", SourceID: "stockcn_minute_http", Weight: 1},
		{Provider: "tdx", SourceID: "normal_7709", Weight: 3}}, five.PrimarySources(), "腾讯只做 fallback")
}

// 按标的固定分配：同一路由、同一标的每次都分到同一来源；沪深 300 量级的标的在三家 primary 之间按权重 3:2:1 分配，腾讯不分到标的。
func TestStockCNAssignmentIsStablePerSubjectAndSpreadsLoad(t *testing.T) {
	routeID, sources, err := stockCNAssignmentRoute("5m")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_5m", routeID)
	counts := map[string]int{}
	for index := 0; index < 300; index++ {
		subject := fmt.Sprintf("%06d.XSHG", 600000+index)
		first, ok := stockSourceForSubject(sources, routeID, subject)
		require.True(t, ok)
		again, _ := stockSourceForSubject(sources, routeID, subject)
		require.Equal(t, first, again, "同一标的每次分到同一来源")
		counts[first.Provider]++
	}
	require.Len(t, counts, 3)
	for provider, want := range map[string]float64{"tdx": 150, "eastmoney": 100, "sina": 50} {
		require.InDelta(t, want, counts[provider], 25, provider)
	}
	require.Greater(t, counts["tdx"], counts["eastmoney"], "tdx 权重最高")
}

func TestStockCNRouteValidation(t *testing.T) {
	provider := stockCNRouteProvider{ID: "eastmoney", SourceID: "stockcn_http", Weight: 1, Role: "primary"}
	cases := map[string]struct {
		route stockCNRoute
		want  string
	}{
		"没有 primary": {stockCNRoute{MarketID: StockCNSpaceID, Klines: []stockCNKlineRoute{{Frequency: "5m", Providers: []stockCNRouteProvider{{ID: "tdx", Weight: 1, Role: "fallback"}}}}}, "至少要有一个 primary"},
		"频率重复":       {stockCNRoute{MarketID: StockCNSpaceID, Klines: []stockCNKlineRoute{{Frequency: "5m", Providers: []stockCNRouteProvider{provider}}, {Frequency: "5m", Providers: []stockCNRouteProvider{provider}}}}, "重复"},
		"频率不规范":      {stockCNRoute{MarketID: StockCNSpaceID, Klines: []stockCNKlineRoute{{Frequency: "5min", Providers: []stockCNRouteProvider{provider}}}}, "无效"},
		"角色无效":       {stockCNRoute{MarketID: StockCNSpaceID, Klines: []stockCNKlineRoute{{Frequency: "5m", Providers: []stockCNRouteProvider{{ID: "eastmoney", Weight: 1, Role: "shadow"}}}}}, "role"},
		"市场不对":       {stockCNRoute{MarketID: "crypto", Klines: []stockCNKlineRoute{{Frequency: "5m", Providers: []stockCNRouteProvider{provider}}}}, "market_id"},
	}
	for name, tc := range cases {
		require.ErrorContains(t, tc.route.Validate(), tc.want, name)
	}
}
