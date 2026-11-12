package marketfetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// 比对测试（路由与流水线通用化）：路由 ID、A 股标的分到的主数据源、函数个数估算、并发与熔断推导、采集计划内容与请求，
// 必须与 testdata 里记录的逐项相同。记录文件是在通用化改造之前的代码上生成的（那时的入口是 requestKlineRouteID、
// stockCNAssignmentRoute、StockCNAutoSize、StockCNFetchLimits）；要重新生成时设 MOOX_UPDATE_GOLDEN=1。
const routeBaselineFile = "testdata/route_baseline.json"

// baselineRouteID 返回流水线（或推送路径的提交器，fixedRouteID 为空）写进行与指标的路由 ID。
// 单一来源的数据源：crypto 是币安，A 股的指数、可转债用东方财富。
func baselineRouteID(t *testing.T, marketID string, instrument marketdata.InstrumentType, fixedRouteID, frequency string) string {
	provider := map[string]string{"crypto": "binance", "stockcn": "eastmoney"}[marketID]
	routeID, err := klineRouteID(marketID, instrument, provider, fixedRouteID, frequency)
	require.NoError(t, err)
	return routeID
}

// baselineAssign 返回 A 股某个频率下标的分到的主数据源，以及这只标的的回落顺序。
func baselineAssign(t *testing.T, frequency, subject string) (string, string, []string) {
	routeID, sources, err := routeAssignment("stockcn", marketdata.InstrumentEquity, frequency)
	require.NoError(t, err)
	selected, ok := assignedSource(sources, routeID, subject)
	require.True(t, ok)
	route, found, err := RouteFor("stockcn", marketdata.InstrumentEquity)
	require.NoError(t, err)
	require.True(t, found)
	kline, err := route.Kline(frequency)
	require.NoError(t, err)
	return selected.Provider, selected.SourceID, normalizeCandidateChain(kline.ProviderChain(), selected.Provider)
}

func baselineAutoSize(sizing FleetSizing) func(TaskGroup, int) int {
	return RouteAutoSize("stockcn", sizing)
}

func baselineFetchLimits() func(TaskGroup, PlanFetch) PlanFetch { return RouteFetchLimits("stockcn") }

func baselineSubjects() []string {
	subjects := []string{"000001.XSHE", "000002.XSHE", "000333.XSHE", "000651.XSHE", "000858.XSHE", "002415.XSHE", "002594.XSHE", "300059.XSHE",
		"300750.XSHE", "301236.XSHE", "600000.XSHG", "600036.XSHG", "600519.XSHG", "600900.XSHG", "601318.XSHG", "601398.XSHG", "601899.XSHG",
		"603259.XSHG", "688981.XSHG", "688041.XSHG", "430047.XBSE", "832735.XBSE", "920118.XBSE"}
	for index := 0; index < 20; index++ {
		subjects = append(subjects, fmt.Sprintf("%06d.XSHG", 600100+index*7))
	}
	return subjects
}

type baselineAssignment struct {
	Provider string   `json:"provider"`
	SourceID string   `json:"source_id"`
	Chain    []string `json:"chain"`
}

type baselineFunction struct {
	Region      string     `json:"region,omitempty"`
	Executor    string     `json:"executor,omitempty"`
	Cron        string     `json:"cron,omitempty"`
	Market      PlanMarket `json:"market"`
	Fetch       PlanFetch  `json:"fetch"`
	Subjects    int        `json:"subjects"`
	PlanSHA256  string     `json:"plan_sha256"`
	RequestKeys []string   `json:"requests"`
}

type routeBaseline struct {
	RouteIDs    map[string]string                        `json:"route_ids"`
	Assignments map[string]map[string]baselineAssignment `json:"assignments"`
	FleetSizes  map[string]int                           `json:"fleet_sizes"`
	FetchLimits map[string]PlanFetch                     `json:"fetch_limits"`
	Datasets    map[string]PlanDatasetState              `json:"datasets"`
	Functions   map[string]baselineFunction              `json:"functions"`
}

func baselineSHA(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestRouteBaseline(t *testing.T) {
	got := routeBaseline{RouteIDs: map[string]string{}, Assignments: map[string]map[string]baselineAssignment{}, FleetSizes: map[string]int{},
		FetchLimits: map[string]PlanFetch{}, Datasets: map[string]PlanDatasetState{}, Functions: map[string]baselineFunction{}}

	// 一、路由 ID 的三种取值。
	for _, frequency := range []string{"1m", "5m", "1d", "5M", " 1D ", "1h", "bogus"} {
		got.RouteIDs["stockcn/equity/"+frequency] = baselineRouteID(t, "stockcn", marketdata.InstrumentEquity, "", frequency)
		got.RouteIDs["stockcn/(未写品种)/"+frequency] = baselineRouteID(t, "stockcn", "", "", frequency)
		got.RouteIDs["crypto/spot/"+frequency] = baselineRouteID(t, "crypto", marketdata.InstrumentSpot, "crypto_spot_kline", frequency)
		got.RouteIDs["crypto/swap/"+frequency] = baselineRouteID(t, "crypto", marketdata.InstrumentSwap, "crypto_swap_kline", frequency)
		got.RouteIDs["crypto/swap/推送/"+frequency] = baselineRouteID(t, "crypto", marketdata.InstrumentSwap, "", frequency)
		got.RouteIDs["stockcn/index/"+frequency] = baselineRouteID(t, "stockcn", marketdata.InstrumentIndex, "stockcn_index_kline", frequency)
		got.RouteIDs["stockcn/convertible_bond/"+frequency] = baselineRouteID(t, "stockcn", marketdata.InstrumentConvertibleBond, "stockcn_convertible_bond_kline", frequency)
		got.RouteIDs["stockcn/index/推送/"+frequency] = baselineRouteID(t, "stockcn", marketdata.InstrumentIndex, "", frequency)
	}

	// 二、A 股标的分到的主数据源与回落顺序。
	for _, frequency := range []string{"1m", "5m", "1d"} {
		got.Assignments[frequency] = map[string]baselineAssignment{}
		for _, subject := range baselineSubjects() {
			provider, sourceID, chain := baselineAssign(t, frequency, subject)
			got.Assignments[frequency][subject] = baselineAssignment{Provider: provider, SourceID: sourceID, Chain: chain}
		}
	}

	// 三、函数个数估算、并发与熔断推导。
	sizing := FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 8}
	stockFetch := PlanFetch{Concurrency: 50, RPS: 50, Burst: 50, FeedConcurrency: 50, RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, NotBeforeMS: 800}
	for _, frequency := range []string{"1m", "5m", "1d", "15m"} {
		for _, subjects := range []int{50, 300, 1000, 5400} {
			got.FleetSizes[fmt.Sprintf("%s/%d", frequency, subjects)] = baselineAutoSize(sizing)(TaskGroup{Frequency: frequency}, subjects)
		}
		got.FetchLimits[frequency] = baselineFetchLimits()(TaskGroup{Frequency: frequency}, stockFetch)
	}
	got.FetchLimits["5m/指数分组"] = baselineFetchLimits()(TaskGroup{Frequency: "5m", MarketID: "stockcn", InstrumentType: "index", MarketType: "index"}, stockFetch)

	// 四、采集计划：A 股（股票 5m、1d 加一个指数分组）与 crypto（现货、永续）。
	equities := make([]string, 0, 300)
	for index := 0; index < 300; index++ {
		equities = append(equities, fmt.Sprintf("%06d.XSHG", 600000+index))
	}
	stockOptions := PlanOptions{
		SpaceID: "stockcn", Markets: map[string][]string{"equity": {"ap-nanjing"}, "index": {"ap-nanjing"}}, SymbolsPerFunction: 50, DefaultLatenessS: 90,
		Fetch: stockFetch, AutoSize: baselineAutoSize(sizing), DeriveFetch: baselineFetchLimits(),
	}
	stockGroups := []TaskGroup{
		{Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", DatasetID: "ds_cn_5m", Frequency: "5m", Subjects: equities},
		{Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", DatasetID: "ds_cn_1d", Frequency: "1d", Subjects: equities,
			OutputFields: []string{"open", "close", "volume", "amount"}},
		{Provider: "eastmoney", SourceID: "index_http", MarketType: "index", MarketID: "stockcn", InstrumentType: "index", DatasetID: "ds_cn_index_5m", Frequency: "5m",
			Subjects: []string{"000300.XSHG", "000905.XSHG", "399006.XSHE"}},
	}
	cryptoFetch := PlanFetch{Concurrency: 20, RPS: 40, Burst: 80, FeedConcurrency: 10, RequestTimeoutMS: 2000}
	spotFetch, swapFetch := cryptoFetch, cryptoFetch
	spotFetch.NotBeforeMS, swapFetch.NotBeforeMS = 3000, 8000
	cryptoOptions := PlanOptions{
		SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong", "ap-singapore"}}, SymbolsPerFunction: 2, DefaultLatenessS: 15,
		Fetch: cryptoFetch, MarketFetch: map[string]PlanFetch{"spot": spotFetch, "swap": swapFetch}, MarketLatenessS: map[string]int{"swap": 20},
	}
	pairs := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "1000PEPE-USDT", "DOGE-USDT"}
	cryptoGroups := []TaskGroup{
		{Provider: "binance", SourceID: "spot_http", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds_spot_1m", Frequency: "1m", Subjects: pairs},
		{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "swap", DatasetID: "ds_swap_1m", Frequency: "1m", Subjects: pairs},
		{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "swap", DatasetID: "ds_swap_1h", Frequency: "1h", Subjects: pairs[:3]},
	}
	// 2026-11-11 00:00 UTC：1m、5m、1h、1d 同时到期。
	scheduled := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		options PlanOptions
		groups  []TaskGroup
	}{{stockOptions, stockGroups}, {cryptoOptions, cryptoGroups}} {
		state, err := BuildDesiredState(item.options, item.groups, nil)
		require.NoError(t, err)
		for key, dataset := range state.Datasets {
			got.Datasets[item.options.SpaceID+"/"+key] = dataset.PlanDatasetState
		}
		for name, function := range state.Functions {
			subjects := map[string]struct{}{}
			for index, dataset := range function.Plan.Datasets {
				for _, subject := range dataset.Copies[0].Subjects {
					subjects[subject.ID] = struct{}{}
				}
				function.Plan.Datasets[index].Copies[0].From = 1
			}
			requests, err := function.Plan.BuildRequests(item.options.SpaceID, name, "request", scheduled, nil)
			require.NoError(t, err)
			keys := make([]string, 0, len(requests))
			for _, planned := range requests {
				request := planned.Request
				keys = append(keys, fmt.Sprintf("%s provider=%q source=%q market=%s instrument=%s market_type=%s items=%d first=%s/%s deadline=%s sha=%s",
					planned.Frequency, request.Provider, request.SourceID, request.MarketID, request.InstrumentType, request.MarketType, len(request.Items),
					request.Items[0].SubjectID, request.Items[0].Symbol, planned.StorageDeadline.Format(time.RFC3339), baselineSHA(request)))
			}
			got.Functions[name] = baselineFunction{Region: function.Region, Executor: function.Executor, Cron: function.Cron, Market: function.Plan.Market,
				Fetch: function.Plan.Fetch, Subjects: len(subjects), PlanSHA256: baselineSHA(function.Plan), RequestKeys: keys}
		}
	}

	raw, err := json.MarshalIndent(got, "", "  ")
	require.NoError(t, err)
	raw = append(raw, '\n')
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(routeBaselineFile), 0o755))
		require.NoError(t, os.WriteFile(routeBaselineFile, raw, 0o644))
		return
	}
	want, err := os.ReadFile(routeBaselineFile)
	require.NoError(t, err)
	require.Equal(t, string(want), string(raw), "路由 ID、来源分配、函数个数或采集计划与改造前记录的不一致")
	// 抽查几项，防止记录文件本身被改坏。
	require.Equal(t, "stockcn_equity_kline_5m", got.RouteIDs["stockcn/equity/5M"])
	require.Equal(t, "binance_swap_kline_1h", got.RouteIDs["crypto/swap/1h"])
	require.Equal(t, "stockcn_index_kline", got.RouteIDs["stockcn/index/5m"])
	require.Equal(t, 38, got.FleetSizes["5m/300"])
}
