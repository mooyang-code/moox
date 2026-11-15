package marketfetch

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// 比对测试（路由与流水线通用化）：路由 ID、A 股标的分到的主数据源，必须与 testdata 里记录的逐项相同。
// 同一个记录文件里的函数个数估算、并发与熔断推导、采集计划内容与请求，由控制面（fetchplan）的 TestPlanRouteBaseline 比对。记录文件是在通用化改造之前的代码上生成的（那时的入口是 requestKlineRouteID、
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

// routeBaseline 是记录文件的内容。后四段属于控制面，这里原样保留、不解码。
type routeBaseline struct {
	RouteIDs    map[string]string                        `json:"route_ids"`
	Assignments map[string]map[string]baselineAssignment `json:"assignments"`
	FleetSizes  json.RawMessage                          `json:"fleet_sizes"`
	FetchLimits json.RawMessage                          `json:"fetch_limits"`
	Datasets    json.RawMessage                          `json:"datasets"`
	Functions   json.RawMessage                          `json:"functions"`
}

func TestRouteBaseline(t *testing.T) {
	got := routeBaseline{RouteIDs: map[string]string{}, Assignments: map[string]map[string]baselineAssignment{}}

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

	recorded, err := os.ReadFile(routeBaselineFile)
	require.NoError(t, err)
	var want routeBaseline
	require.NoError(t, json.Unmarshal(recorded, &want))
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		// 只重写自己的两段，控制面的四段保持原样。
		want.RouteIDs, want.Assignments = got.RouteIDs, got.Assignments
		raw, err := json.MarshalIndent(want, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(routeBaselineFile, append(raw, '\n'), 0o644))
		return
	}
	require.Equal(t, want.RouteIDs, got.RouteIDs, "路由 ID 与改造前记录的不一致")
	require.Equal(t, want.Assignments, got.Assignments, "来源分配与改造前记录的不一致")
	// 抽查几项，防止记录文件本身被改坏。
	require.Equal(t, "stockcn_equity_kline_5m", got.RouteIDs["stockcn/equity/5M"])
	require.Equal(t, "binance_swap_kline_1h", got.RouteIDs["crypto/swap/1h"])
	require.Equal(t, "stockcn_index_kline", got.RouteIDs["stockcn/index/5m"])
}
