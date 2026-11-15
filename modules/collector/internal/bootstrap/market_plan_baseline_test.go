package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/stretchr/testify/require"
)

// 批 2 的基线金样（真实市场配置这一层）。
//
// 四个市场各用编进二进制的 market.yaml、route.yaml、sources/*.yaml（部署配置只写空间名，其余全取默认值），
// 按 registerFetchPlanManager 里 Factory 的做法拼出采集计划参数，记下：
//   - 计划参数：每函数标的数、函数个数估算、出口 IP 去重、拉取参数（空间与各市场）、截止、新数据集的采集方式初值；
//   - 由这些参数生成的期望状态：数据集、每个函数的地域、执行端、cron 与 Plan JSON（下发给函数的原文）。
//
// 采集方式收成一列三值之后，计划参数里“采集方式初值”只剩 default_ingest 一项（原来还有换算前的 default_mode 两项），
// 其余内容与改之前逐字节相同。要重新生成时设 MOOX_UPDATE_GOLDEN=1。

type marketPlanBaselineFunction struct {
	Region   string `json:"region,omitempty"`
	Executor string `json:"executor"`
	Cron     string `json:"cron,omitempty"`
	Plan     string `json:"plan"`
}

type marketPlanBaselineState struct {
	// Ingest 是这一组期望状态用的执行端（scf、stream、local）。
	Ingest    string                                `json:"ingest"`
	Datasets  map[string]fetchplan.PlanDatasetState `json:"datasets"`
	Functions map[string]marketPlanBaselineFunction `json:"functions"`
}

type marketPlanBaseline struct {
	SpaceID            string                           `json:"space_id"`
	SymbolsPerFunction int                              `json:"symbols_per_function"`
	AutoSize           *fetchplan.FleetSizing           `json:"auto_size"`
	EgressBalance      *egressbalance.Config            `json:"egress_balance"`
	DefaultLatenessS   int                              `json:"default_lateness_s"`
	MarketLatenessS    map[string]int                   `json:"market_lateness_s"`
	Fetch              marketfetch.PlanFetch            `json:"fetch"`
	MarketFetch        map[string]marketfetch.PlanFetch `json:"market_fetch"`
	// DefaultIngest 是这个市场 K 线任务的默认采集方式（market.yaml 的 ingest），也就是计划里的执行端。
	DefaultIngest string                    `json:"default_ingest"`
	States        []marketPlanBaselineState `json:"states"`
}

func marketPlanSubjects(format string, start, count int) []string {
	subjects := make([]string, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, fmt.Sprintf(format, start+i))
	}
	return subjects
}

func TestMarketPlansMatchBaseline(t *testing.T) {
	pairs := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "1000PEPE-USDT", "DOGE-USDT"}
	cases := []struct {
		space   string
		markets map[string][]string
		groups  []fetchplan.TaskGroup
		// ingests 是要记的执行端；空表示只记市场的默认值。
		ingests []string
	}{
		{space: "stockcn", markets: map[string][]string{"equity": {"ap-nanjing"}, "index": {"ap-nanjing"}}, groups: []fetchplan.TaskGroup{
			{Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", DatasetID: "ds_cn_5m", Frequency: "5m",
				Subjects: marketPlanSubjects("%06d.XSHG", 600000, 300)},
			{Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", DatasetID: "ds_cn_1d", Frequency: "1d",
				Subjects: marketPlanSubjects("%06d.XSHG", 600000, 300), OutputFields: []string{"open", "close", "volume", "amount"}},
			{Provider: "eastmoney", SourceID: "index_http", MarketType: "index", MarketID: "stockcn", InstrumentType: "index", DatasetID: "ds_cn_index_5m", Frequency: "5m",
				Subjects: []string{"000300.XSHG", "000905.XSHG", "399006.XSHE"}},
		}},
		{space: "stockhk", markets: map[string][]string{"equity": {"ap-nanjing"}}, groups: []fetchplan.TaskGroup{
			{Provider: "eastmoney", SourceID: "stockhk_http", MarketType: "equity", MarketID: "stockhk", InstrumentType: "equity", DatasetID: "ds_hk_5m", Frequency: "5m",
				Subjects: marketPlanSubjects("%05d.XHKG", 1, 95)},
		}},
		{space: "stockus", markets: map[string][]string{"equity": {"ap-nanjing"}}, groups: []fetchplan.TaskGroup{
			{Provider: "eastmoney", SourceID: "stockus_http", MarketType: "equity", MarketID: "stockus", InstrumentType: "equity", DatasetID: "ds_us_5m", Frequency: "5m",
				Subjects: marketPlanSubjects("S%03d.XNYS", 1, 505)},
		}},
		// crypto 的默认值是推送；另外记一组全部走云函数的（现货、永续各自的取数时刻与截止只在云函数计划里体现）。
		{space: "crypto", markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong", "ap-singapore"}}, groups: []fetchplan.TaskGroup{
			{Provider: "binance", SourceID: "spot_http", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot", DatasetID: "ds_spot_1m", Frequency: "1m", Subjects: pairs},
			{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "swap", DatasetID: "ds_swap_1m", Frequency: "1m", Subjects: pairs},
			{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "swap", DatasetID: "ds_swap_1h", Frequency: "1h", Subjects: pairs[:3]},
		}, ingests: []string{marketfetch.PlanExecutorStreaming, marketfetch.PlanExecutorSCF, marketfetch.PlanExecutorLocal}},
	}
	var got []marketPlanBaseline
	for _, item := range cases {
		defaults := defaultSCFPlan()
		plan := SCFPlanConfig{Fetch: defaults.Fetch, SymbolsPerFunction: defaults.SymbolsPerFunction, Spaces: map[string]SCFPlanSpaceConfig{item.space: {}}}
		plan.mergeSpaceDefaults(defaults.Spaces)
		latenessSeconds, err := defaultLatenessSeconds(item.space)
		require.NoError(t, err)
		defaultIngest := markets.DefaultIngest(item.space)
		record := marketPlanBaseline{
			SpaceID: item.space, SymbolsPerFunction: plan.symbolsPerFunction(item.space), AutoSize: plan.autoSizing(item.space),
			DefaultLatenessS: latenessSeconds, MarketLatenessS: plan.marketLateness(item.space),
			Fetch: plan.fetchFor(item.space, ""), MarketFetch: plan.marketFetches(item.space),
			DefaultIngest: defaultIngest,
		}
		if balance := plan.Spaces[item.space].EgressBalance; balance != nil {
			config := balance.config()
			record.EgressBalance = &config
		}
		ingests := item.ingests
		if len(ingests) == 0 {
			ingests = []string{defaultIngest}
		}
		require.Contains(t, ingests, defaultIngest, "%s 的默认采集方式要在记录里", item.space)
		for _, ingest := range ingests {
			// 与 registerFetchPlanManager 里的 Factory 一致。
			options := fetchplan.PlanOptions{
				StreamLatenessS: defaultStreamConfig().LatenessS,
				SpaceID:         item.space, Markets: item.markets, SymbolsPerFunction: plan.symbolsPerFunction(item.space),
				Fetch: plan.fetchFor(item.space, ""), MarketFetch: plan.marketFetches(item.space),
				DefaultLatenessS: latenessSeconds, MarketLatenessS: plan.marketLateness(item.space), AssumedEgressIPs: plan.AssumedEgressIPs,
				DeriveFetch: fetchplan.RouteFetchLimits(item.space),
			}
			if sizing := plan.autoSizing(item.space); sizing != nil {
				options.AutoSize = fetchplan.RouteAutoSize(item.space, *sizing)
			}
			// 采集方式记在任务上：这一组的任务组都用 ingest。
			groups := append([]fetchplan.TaskGroup(nil), item.groups...)
			for index := range groups {
				groups[index].Ingest = ingest
			}
			desired, err := fetchplan.BuildDesiredState(options, groups, nil)
			require.NoError(t, err, "%s %s", item.space, ingest)
			state := marketPlanBaselineState{Ingest: ingest, Datasets: map[string]fetchplan.PlanDatasetState{}, Functions: map[string]marketPlanBaselineFunction{}}
			for key, dataset := range desired.Datasets {
				state.Datasets[key] = dataset.PlanDatasetState
			}
			for name, function := range desired.Functions {
				// 生效起点由对账选定，这里固定成 1，Plan 才能编码。
				for index := range function.Plan.Datasets {
					function.Plan.Datasets[index].Copies[0].From = 1
				}
				encoded, err := marketfetch.EncodePlan(function.Plan)
				require.NoError(t, err, name)
				state.Functions[name] = marketPlanBaselineFunction{Region: function.Region, Executor: function.Executor, Cron: function.Cron, Plan: encoded}
			}
			require.NotEmpty(t, state.Functions, "%s %s", item.space, ingest)
			record.States = append(record.States, state)
		}
		got = append(got, record)
	}
	raw, err := json.MarshalIndent(got, "", " ")
	require.NoError(t, err)
	raw = append(raw, '\n')
	path := filepath.Join("testdata", "market_plans.golden.json")
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, raw, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(want), string(raw), "按真实市场配置生成的计划参数或 Plan JSON 与基线不一致")
}
