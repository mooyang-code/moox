package bootstrap

import (
	"fmt"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// hkPlanOptions 按 registerFetchPlanManager 里 Factory 的做法，由部署配置拼出港股的采集计划参数。
func hkPlanOptions(plan SCFPlanConfig) marketfetch.PlanOptions {
	options := marketfetch.PlanOptions{
		SpaceID: "stockhk", Markets: map[string][]string{"equity": {"ap-nanjing"}}, SymbolsPerFunction: plan.symbolsPerFunction("stockhk"),
		Fetch: plan.fetchFor("stockhk", ""), MarketFetch: plan.marketFetches("stockhk"), DefaultLatenessS: 90,
		DeriveFetch: marketfetch.RouteFetchLimits("stockhk"),
	}
	if sizing := plan.autoSizing("stockhk"); sizing != nil {
		options.AutoSize = marketfetch.RouteAutoSize("stockhk", *sizing)
	}
	return options
}

func hkGroups(subjects int) []marketfetch.TaskGroup {
	ids := make([]string, 0, subjects)
	for i := 1; i <= subjects; i++ {
		ids = append(ids, fmt.Sprintf("%05d.XHKG", i))
	}
	return []marketfetch.TaskGroup{{Provider: "eastmoney", SourceID: "stockhk_http", MarketType: "equity", MarketID: "stockhk", InstrumentType: "equity",
		DatasetID: "ds_hk_5m", Frequency: "5m", Subjects: ids}}
}

// 港股的采集计划参数：部署配置只写代码包与命名空间时，market.yaml 的默认值生效——固定每函数 25 只、
// 不估算函数个数、不做出口 IP 去重、收盘后等 5 秒；95 只标的得到 4 个函数，且通过结算延迟（5 秒）的校验。
// 路由、结算延迟、东方财富的限速都读编进二进制的 config/markets/stockhk/。
func TestStockHKPlanUsesFourFunctionsAndWaitsFiveSeconds(t *testing.T) {
	defaults := defaultSCFPlan()
	plan := SCFPlanConfig{Fetch: defaults.Fetch, SymbolsPerFunction: defaults.SymbolsPerFunction,
		Spaces: map[string]SCFPlanSpaceConfig{"stockhk": {PackageName: "moox-collector-stockhk-plan", Namespace: "moox-stockhk-plan"}}}
	plan.mergeSpaceDefaults(defaults.Spaces)

	require.Equal(t, 25, plan.symbolsPerFunction("stockhk"))
	require.Nil(t, plan.autoSizing("stockhk"), "港股不按负载估算函数个数")
	require.Nil(t, plan.Spaces["stockhk"].EgressBalance, "港股不做出口 IP 去重")
	require.Equal(t, 5000, plan.fetchFor("stockhk", "equity").NotBeforeMS)
	require.Equal(t, marketfetch.DatasetMode{Mode: marketfetch.IngestPull, Executor: marketfetch.PlanExecutorSCF}, plan.defaultModeFor("stockhk"))

	desired, err := marketfetch.BuildDesiredState(hkPlanOptions(plan), hkGroups(95), nil)
	require.NoError(t, err)
	require.Len(t, desired.Functions, 4)
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
	require.Equal(t, 95, total)
	sort.Sort(sort.Reverse(sort.IntSlice(sizes)))
	require.Equal(t, []int{25, 25, 25, 20}, sizes)

	// 100 只正好 4 个函数，101 只要第 5 个。
	for subjects, want := range map[int]int{25: 1, 100: 4, 101: 5} {
		desired, err = marketfetch.BuildDesiredState(hkPlanOptions(plan), hkGroups(subjects), nil)
		require.NoError(t, err)
		require.Len(t, desired.Functions, want, "%d 只标的", subjects)
	}
}

// moox.toml 的 [scf_plan] 把港股的 not_before_ms 改小到结算延迟以下时，生成计划直接报错（目标那根每期都会被丢掉）。
func TestStockHKPlanRejectsNotBeforeBelowTheSettleDelay(t *testing.T) {
	defaults := defaultSCFPlan()
	plan := SCFPlanConfig{Fetch: defaults.Fetch, SymbolsPerFunction: defaults.SymbolsPerFunction,
		Spaces: map[string]SCFPlanSpaceConfig{"stockhk": {Fetch: &SCFPlanFetchConfig{NotBeforeMS: 800}}}}
	plan.mergeSpaceDefaults(defaults.Spaces)
	_, err := marketfetch.BuildDesiredState(hkPlanOptions(plan), hkGroups(95), nil)
	require.ErrorContains(t, err, "小于路由文件的结算延迟 5000 毫秒")
}
