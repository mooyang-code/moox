package marketfetch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// writeMarketFile 在覆盖目录里写一个市场数据文件（name 形如 stockcn/route.yaml），并让 MOOX_MARKET_CONFIG_DIR 指向这个目录。
func writeMarketFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	t.Setenv(MarketConfigDirEnv, dir)
}

func TestLoadRouteSplitsKlineRoutesByFrequency(t *testing.T) {
	writeMarketFile(t, t.TempDir(), "stockcn/route.yaml", `market_id: stockcn
instrument_type: equity
settle_delay_ms: 1200
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
`)

	route, found, err := LoadRoute("stockcn")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, marketdata.InstrumentEquity, route.Instrument())
	require.Equal(t, 1200*time.Millisecond, route.SettleDelay())
	require.Equal(t, []string{"eastmoney", "sina"}, route.InstrumentProviders())
	require.Equal(t, []string{"eastmoney", "sina", "tdx"}, route.ProviderIDs())

	minute, err := route.Kline("1m")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_1m", minute.RouteID())
	require.Equal(t, []string{"sina", "eastmoney", "tdx"}, minute.ProviderChain())
	require.Equal(t, []RouteSource{{Provider: "eastmoney", SourceID: "stockcn_http", Weight: 3}, {Provider: "sina", SourceID: "stockcn_minute_http", Weight: 2}}, minute.PrimarySources())

	five, err := route.Kline("5m")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_5m", five.RouteID())
	require.Equal(t, []string{"eastmoney", "tdx"}, five.ProviderChain())
	require.Equal(t, []RouteSource{{Provider: "eastmoney", SourceID: "stockcn_http", Weight: 1}}, five.PrimarySources())

	_, err = route.Kline("15m")
	require.ErrorContains(t, err, "没有 15m 的 K 线路由")
}

func TestLoadSourceRuntimesUsesPackagedFeedPolicies(t *testing.T) {
	route, found, err := LoadRoute("stockcn")
	require.NoError(t, err)
	require.True(t, found)
	providers, err := LoadSourceRuntimes(route)
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
	route, found, err := LoadRoute("stockcn")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 800*time.Millisecond, route.SettleDelay(), "A 股的结算延迟是 0.8 秒，与 market.yaml 的 not_before_ms 一致")
	minute, err := route.Kline("1m")
	require.NoError(t, err)
	require.Equal(t, []string{"sina", "tencent", "tdx", "eastmoney"}, minute.ProviderChain())
	five, err := route.Kline("5m")
	require.NoError(t, err)
	require.Equal(t, []string{"tdx", "eastmoney", "sina", "tencent"}, five.ProviderChain())
	// PrimarySources 按来源名排序，与候选链的回落顺序无关；分配只看权重。
	require.Equal(t, []RouteSource{{Provider: "eastmoney", SourceID: "stockcn_http", Weight: 2},
		{Provider: "tdx", SourceID: "normal_7709", Weight: 4}}, five.PrimarySources(), "新浪、腾讯只做 fallback")
	// 日线：通达信为主，东方财富为备。
	daily, err := route.Kline("1d")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_1d", daily.RouteID())
	require.Equal(t, []string{"tdx", "eastmoney"}, daily.ProviderChain())
	require.Equal(t, []RouteSource{{Provider: "tdx", SourceID: "normal_7709", Weight: 1}}, daily.PrimarySources())
}

// 按标的固定分配：同一路由、同一标的每次都分到同一来源；沪深 300 量级的标的在两家 primary 之间按权重 4:2 分配，新浪、腾讯不分到标的。
func TestRouteAssignmentIsStablePerSubjectAndSpreadsLoad(t *testing.T) {
	routeID, sources, err := routeAssignment("stockcn", marketdata.InstrumentEquity, "5m")
	require.NoError(t, err)
	require.Equal(t, "stockcn_equity_kline_5m", routeID)
	counts := map[string]int{}
	for index := 0; index < 300; index++ {
		subject := fmt.Sprintf("%06d.XSHG", 600000+index)
		first, ok := assignedSource(sources, routeID, subject)
		require.True(t, ok)
		again, _ := assignedSource(sources, routeID, subject)
		require.Equal(t, first, again, "同一标的每次分到同一来源")
		counts[first.Provider]++
	}
	require.Len(t, counts, 2)
	for provider, want := range map[string]float64{"tdx": 200, "eastmoney": 100} {
		require.InDelta(t, want, counts[provider], 25, provider)
	}
	require.Greater(t, counts["tdx"], counts["eastmoney"], "tdx 权重最高")
}

// 固定 A 股若干标的分到的主数据源：路由 ID 是哈希种子，路由 ID 或哈希算法变了这里会失败（所有标的会重新分配）。
func TestStockCNSubjectsKeepTheirPrimarySource(t *testing.T) {
	want := map[string]map[string]string{
		"5m": {"600000.XSHG": "tdx", "600519.XSHG": "eastmoney", "000001.XSHE": "eastmoney", "000858.XSHE": "tdx", "300750.XSHE": "tdx",
			"601318.XSHG": "eastmoney", "688981.XSHG": "tdx", "430047.XBSE": "tdx"},
		"1m": {"600000.XSHG": "tencent", "600519.XSHG": "sina", "000001.XSHE": "sina", "000858.XSHE": "sina", "300750.XSHE": "sina",
			"601318.XSHG": "sina", "688981.XSHG": "tencent", "430047.XBSE": "tencent"},
		"1d": {"600000.XSHG": "tdx", "600519.XSHG": "tdx", "430047.XBSE": "tdx"},
	}
	for frequency, subjects := range want {
		routeID, sources, err := routeAssignment("stockcn", marketdata.InstrumentEquity, frequency)
		require.NoError(t, err)
		for subject, provider := range subjects {
			selected, ok := assignedSource(sources, routeID, subject)
			require.True(t, ok)
			require.Equal(t, provider, selected.Provider, "%s %s", frequency, subject)
		}
	}
}

// 路由 ID 的三种现有取值：按路由分配的带市场、品种、频率；市场没有路由文件的带数据源、品种、频率；
// 市场有路由文件但品种不归路由管的，用流水线自带的（不带频率），推送路径没有流水线，是空串。
func TestKlineRouteIDKeepsTheThreeExistingForms(t *testing.T) {
	for _, tc := range []struct {
		marketID   string
		instrument marketdata.InstrumentType
		provider   string
		fixed      string
		frequency  string
		want       string
	}{
		{"stockcn", marketdata.InstrumentEquity, "tdx", "", "5m", "stockcn_equity_kline_5m"},
		{"stockcn", marketdata.InstrumentEquity, "tdx", "", " 1D ", "stockcn_equity_kline_1d"},
		{"stockcn", "", "", "", "1m", "stockcn_equity_kline_1m"},
		{"crypto", marketdata.InstrumentSpot, "binance", "crypto_spot_kline", "1h", "binance_spot_kline_1h"},
		{"crypto", marketdata.InstrumentSwap, "binance", "crypto_swap_kline", "1w", "binance_swap_kline_1w"},
		{"crypto", marketdata.InstrumentSwap, "binance", "", "1m", "binance_swap_kline_1m"},
		{"crypto", "", "binance", "", "1m", "binance_spot_kline_1m"},
	} {
		got, err := klineRouteID(tc.marketID, tc.instrument, tc.provider, tc.fixed, tc.frequency)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "%+v", tc)
	}
	// 没有路由文件的市场一律按数据源命名，不要求市场登记过；没写品种时才要查市场的默认品种。
	got, err := klineRouteID("stockjp", marketdata.InstrumentEquity, "x", "", "5m")
	require.NoError(t, err)
	require.Equal(t, "x_equity_kline_5m", got)
	_, err = klineRouteID("stockjp", "", "x", "", "5m")
	require.Error(t, err, "未登记的市场没有默认品种")
}

// 路由只管它写明的品种：同一个市场的其他品种、没有路由文件的市场，来源都是任务或标签指定的那一个。
func TestRouteForOnlyCoversItsInstrument(t *testing.T) {
	for _, tc := range []struct {
		marketID   string
		instrument marketdata.InstrumentType
		want       bool
	}{
		{"stockcn", marketdata.InstrumentEquity, true},
		{"stockcn", "", true},
		{"STOCKCN", marketdata.InstrumentEquity, true},
		{"stockcn", "index", false},
		{"crypto", marketdata.InstrumentSpot, false},
		{"crypto", "", false},
		{"stockjp", marketdata.InstrumentEquity, false},
	} {
		_, found, err := RouteFor(tc.marketID, tc.instrument)
		require.NoError(t, err)
		require.Equal(t, tc.want, found, "%+v", tc)
	}
	for _, market := range []PlanMarket{
		{MarketID: "stockcn", InstrumentType: "equity", Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity"},
		{MarketID: "stockcn", Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity"},
	} {
		provider, sourceID := market.RequestSource()
		require.Empty(t, provider+sourceID, "按路由分配的请求不写来源")
	}
	for _, market := range []PlanMarket{
		{MarketID: "crypto", InstrumentType: "swap", Provider: "binance", SourceID: "swap_http", MarketType: "swap"},
	} {
		provider, sourceID := market.RequestSource()
		require.Equal(t, market.Provider, provider)
		require.Equal(t, market.SourceID, sourceID)
	}
}

func TestRouteValidation(t *testing.T) {
	ms := func(value int) *int { return &value }
	provider := RouteProvider{ID: "eastmoney", SourceID: "stockcn_http", Weight: 1, Role: "primary"}
	klines := []KlineRoute{{Frequency: "5m", Providers: []RouteProvider{provider}}}
	cases := map[string]struct {
		route Route
		want  string
	}{
		"没有 primary": {Route{MarketID: "stockcn", InstrumentType: "equity", SettleDelayMS: ms(800), Klines: []KlineRoute{{Frequency: "5m", Providers: []RouteProvider{{ID: "tdx", Weight: 1, Role: "fallback"}}}}}, "至少要有一个 primary"},
		"频率重复":       {Route{MarketID: "stockcn", InstrumentType: "equity", SettleDelayMS: ms(800), Klines: []KlineRoute{{Frequency: "5m", Providers: []RouteProvider{provider}}, {Frequency: "5m", Providers: []RouteProvider{provider}}}}, "重复"},
		"频率不规范":      {Route{MarketID: "stockcn", InstrumentType: "equity", SettleDelayMS: ms(800), Klines: []KlineRoute{{Frequency: "5min", Providers: []RouteProvider{provider}}}}, "无效"},
		"角色无效":       {Route{MarketID: "stockcn", InstrumentType: "equity", SettleDelayMS: ms(800), Klines: []KlineRoute{{Frequency: "5m", Providers: []RouteProvider{{ID: "eastmoney", Weight: 1, Role: "shadow"}}}}}, "role"},
		"市场不是所在目录的":  {Route{MarketID: "crypto", InstrumentType: "equity", SettleDelayMS: ms(800), Klines: klines}, "market_id"},
		"没写品种":       {Route{MarketID: "stockcn", SettleDelayMS: ms(800), Klines: klines}, "instrument_type"},
		"结算延迟为负":     {Route{MarketID: "stockcn", InstrumentType: "equity", SettleDelayMS: ms(-1), Klines: klines}, "settle_delay_ms"},
		"没写结算延迟":     {Route{MarketID: "stockcn", InstrumentType: "equity", Klines: klines}, "settle_delay_ms 必填"},
	}
	for name, tc := range cases {
		require.ErrorContains(t, tc.route.validate("stockcn"), tc.want, name)
	}
	require.NoError(t, Route{MarketID: "stockcn", InstrumentType: "equity", SettleDelayMS: ms(0), Klines: klines}.validate("stockcn"), "不需要等待时写 0")
	require.NoError(t, Route{MarketID: "stockhk", InstrumentType: "equity", SettleDelayMS: ms(5000), Klines: klines}.validate("stockhk"), "路由的市场等于所在目录的市场即可")
}

// 数据源配置里的 market 必须等于所在目录的市场。
func TestSourceFileMustBelongToItsMarketDirectory(t *testing.T) {
	writeMarketFile(t, t.TempDir(), "stockcn/sources/sina.yaml", `provider:
  id: sina
  market: stockhk
  hosts: [example.com]
  rate_limit: {scope: egress_ip, requests_per_second: 1, burst: 1, max_concurrent: 1, cooldown: 1s, request_timeout: 1s}
`)
	_, _, err := LoadSourceRuntime("stockcn", "sina")
	require.ErrorContains(t, err, `market 必须是 "stockcn"`)
}
