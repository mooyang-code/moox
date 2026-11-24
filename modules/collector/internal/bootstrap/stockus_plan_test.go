package bootstrap

import (
	"fmt"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

// usPlanOptions 按 registerFetchPlanManager 里 Factory 的做法，由部署配置拼出美股的采集计划参数。
func usPlanOptions(plan SCFPlanConfig) fetchplan.PlanOptions {
	options := fetchplan.PlanOptions{
		SpaceID: "stockus", Markets: map[string][]string{"equity": {"ap-nanjing"}}, SymbolsPerFunction: plan.symbolsPerFunction("stockus"),
		Fetch: plan.fetchFor("stockus", ""), MarketFetch: plan.marketFetches("stockus"), DefaultLatenessS: 90,
		DeriveFetch: fetchplan.RouteFetchLimits("stockus"),
	}
	if sizing := plan.autoSizing("stockus"); sizing != nil {
		options.AutoSize = fetchplan.RouteAutoSize("stockus", *sizing)
	}
	return options
}

// usGroups 与 Collector 拼出的美股分组相同：provider 是标签的 sync.source（东方财富）；行情源目录里美股的 K 线只有 CNBC，
// 查不到东方财富的 source_id，分组退回用 provider。K 线来源由路由分配，与这两个值无关。
func usGroups(subjects int) []fetchplan.TaskGroup {
	ids := make([]string, 0, subjects)
	for i := 1; i <= subjects; i++ {
		ids = append(ids, fmt.Sprintf("S%03d.XNYS", i))
	}
	return []fetchplan.TaskGroup{{Provider: "eastmoney", SourceID: "eastmoney", MarketType: "equity", MarketID: "stockus", InstrumentType: "equity",
		DatasetID: "ds_us_5m", Frequency: "5m", Subjects: ids}}
}

// usPlanFromDefaults 是部署配置只写代码包与命名空间时的采集计划配置：其余取 market.yaml 的默认值。
func usPlanFromDefaults() SCFPlanConfig {
	defaults := defaultSCFPlan()
	plan := SCFPlanConfig{Fetch: defaults.Fetch, SymbolsPerFunction: defaults.SymbolsPerFunction,
		Spaces: map[string]SCFPlanSpaceConfig{"stockus": {PackageName: "moox-collector-stockus-plan", Namespace: "moox-stockus-plan"}}}
	plan.mergeSpaceDefaults(defaults.Spaces)
	return plan
}

// 美股的采集计划参数：部署配置只写代码包与命名空间时，market.yaml 的默认值生效——固定每函数 50 只、
// 不估算函数个数、不做出口 IP 去重、收盘后等 5 秒；标普 500 的 503 只标的得到 11 个函数，且通过结算延迟（5 秒）的校验。
// 路由、结算延迟、东方财富的限速都读编进二进制的 config/markets/stockus/。
func TestStockUSPlanUsesElevenFunctionsAndWaitsFiveSeconds(t *testing.T) {
	plan := usPlanFromDefaults()

	require.Equal(t, 50, plan.symbolsPerFunction("stockus"))
	require.Nil(t, plan.autoSizing("stockus"), "美股不按负载估算函数个数")
	require.Nil(t, plan.Spaces["stockus"].EgressBalance, "美股不做出口 IP 去重")
	require.Equal(t, 5000, plan.fetchFor("stockus", "equity").NotBeforeMS)
	require.Equal(t, domain.IngestSCF, markets.DefaultIngest("stockus"))

	desired, err := fetchplan.BuildDesiredState(usPlanOptions(plan), usGroups(503), nil)
	require.NoError(t, err)
	require.Len(t, desired.Functions, 11)
	total := 0
	var sizes []int
	for name, function := range desired.Functions {
		require.Equal(t, "ap-nanjing", function.Region, name)
		require.Equal(t, 5000, function.Plan.Fetch.NotBeforeMS, name)
		// 函数内并发由东方财富的限速配置推导：只有一个主源，等于它的 max_concurrent。
		require.Equal(t, 16, function.Plan.Fetch.Concurrency, name)
		require.Equal(t, 16, function.Plan.Fetch.FeedConcurrency, name)
		subjects := len(function.Plan.Datasets[0].Copies[0].Subjects)
		sizes = append(sizes, subjects)
		total += subjects
	}
	require.Equal(t, 503, total)
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	require.Equal(t, []int{50, 50, 50, 50, 50, 50, 50, 50, 50, 50, 3}, sizes)

	// 纳斯达克 100（101 只）3 个函数；500 只正好 10 个，501 只要第 11 个。
	for subjects, want := range map[int]int{50: 1, 101: 3, 500: 10, 501: 11} {
		desired, err = fetchplan.BuildDesiredState(usPlanOptions(plan), usGroups(subjects), nil)
		require.NoError(t, err)
		require.Len(t, desired.Functions, want, "%d 只标的", subjects)
	}
}

// moox.toml 的 [scf_plan] 把美股的 not_before_ms 改小到结算延迟以下时，生成计划直接报错（目标那根每期都会被丢掉）。
func TestStockUSPlanRejectsNotBeforeBelowTheSettleDelay(t *testing.T) {
	defaults := defaultSCFPlan()
	plan := SCFPlanConfig{Fetch: defaults.Fetch, SymbolsPerFunction: defaults.SymbolsPerFunction,
		Spaces: map[string]SCFPlanSpaceConfig{"stockus": {Fetch: &SCFPlanFetchConfig{NotBeforeMS: 1000}}}}
	plan.mergeSpaceDefaults(defaults.Spaces)
	_, err := fetchplan.BuildDesiredState(usPlanOptions(plan), usGroups(503), nil)
	require.ErrorContains(t, err, "小于路由文件的结算延迟 5000 毫秒")
}
