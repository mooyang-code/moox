package fetchplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

// 比对测试（路由与流水线通用化）的控制面部分：函数个数估算、并发与熔断推导、采集计划内容与请求，
// 必须与记录文件逐项相同。记录文件与执行面（marketfetch）的 TestRouteBaseline 共用，那边比对路由 ID 与来源分配。
// 记录文件是在通用化改造之前的代码上生成的；要重新生成时设 MOOX_UPDATE_GOLDEN=1。
var planRouteBaselineFile = filepath.Join("..", "marketfetch", "testdata", "route_baseline.json")

func baselineAutoSize(sizing FleetSizing) func(TaskGroup, int) int {
	return RouteAutoSize("stockcn", sizing)
}

func baselineFetchLimits() func(TaskGroup, marketfetch.PlanFetch) marketfetch.PlanFetch {
	return RouteFetchLimits("stockcn")
}

type planRouteBaselineFunction struct {
	Region      string                 `json:"region,omitempty"`
	Executor    string                 `json:"executor,omitempty"`
	Cron        string                 `json:"cron,omitempty"`
	Market      marketfetch.PlanMarket `json:"market"`
	Fetch       marketfetch.PlanFetch  `json:"fetch"`
	Subjects    int                    `json:"subjects"`
	PlanSHA256  string                 `json:"plan_sha256"`
	RequestKeys []string               `json:"requests"`
}

// planRouteBaseline 是这边算出来的四段。
type planRouteBaseline struct {
	FleetSizes  map[string]int
	FetchLimits map[string]marketfetch.PlanFetch
	Datasets    map[string]PlanDatasetState
	Functions   map[string]planRouteBaselineFunction
}

// planRouteBaselineRecord 是记录文件的内容，六段都不解码：前两段属于执行面，后四段按字节比对。
// 不能把记录解码成当前的结构体再编码回去比——结构体字段增删改名时两边会一起变，比不出差别。
type planRouteBaselineRecord struct {
	RouteIDs    json.RawMessage `json:"route_ids"`
	Assignments json.RawMessage `json:"assignments"`
	FleetSizes  json.RawMessage `json:"fleet_sizes"`
	FetchLimits json.RawMessage `json:"fetch_limits"`
	Datasets    json.RawMessage `json:"datasets"`
	Functions   json.RawMessage `json:"functions"`
}

func baselineSHA(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestPlanRouteBaseline(t *testing.T) {
	got := planRouteBaseline{FleetSizes: map[string]int{}, FetchLimits: map[string]marketfetch.PlanFetch{}, Datasets: map[string]PlanDatasetState{}, Functions: map[string]planRouteBaselineFunction{}}

	// 三、函数个数估算、并发与熔断推导。
	sizing := FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 8}
	stockFetch := marketfetch.PlanFetch{Concurrency: 50, RPS: 50, Burst: 50, FeedConcurrency: 50, RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, NotBeforeMS: 800}
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
	cryptoFetch := marketfetch.PlanFetch{Concurrency: 20, RPS: 40, Burst: 80, FeedConcurrency: 10, RequestTimeoutMS: 2000}
	spotFetch, swapFetch := cryptoFetch, cryptoFetch
	spotFetch.NotBeforeMS, swapFetch.NotBeforeMS = 3000, 8000
	cryptoOptions := PlanOptions{
		SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-hongkong"}, "swap": {"ap-hongkong", "ap-singapore"}}, SymbolsPerFunction: 2, DefaultLatenessS: 15,
		Fetch: cryptoFetch, MarketFetch: map[string]marketfetch.PlanFetch{"spot": spotFetch, "swap": swapFetch}, MarketLatenessS: map[string]int{"swap": 20},
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
			requests, err := function.Plan.BuildRequests(item.options.SpaceID, name, "request", scheduled)
			require.NoError(t, err)
			keys := make([]string, 0, len(requests))
			for _, planned := range requests {
				request := planned.Request
				keys = append(keys, fmt.Sprintf("%s provider=%q source=%q market=%s instrument=%s market_type=%s items=%d first=%s/%s deadline=%s sha=%s",
					planned.Frequency, request.Provider, request.SourceID, request.MarketID, request.InstrumentType, request.MarketType, len(request.Items),
					request.Items[0].SubjectID, request.Items[0].Symbol, planned.StorageDeadline.Format(time.RFC3339), baselineSHA(request)))
			}
			got.Functions[name] = planRouteBaselineFunction{Region: function.Region, Executor: function.Executor, Cron: function.Cron, Market: function.Plan.Market,
				Fetch: function.Plan.Fetch, Subjects: len(subjects), PlanSHA256: baselineSHA(function.Plan), RequestKeys: keys}
		}
	}

	recorded, err := os.ReadFile(planRouteBaselineFile)
	require.NoError(t, err)
	var want planRouteBaselineRecord
	require.NoError(t, json.Unmarshal(recorded, &want))
	sections := []struct {
		name string
		want *json.RawMessage
		got  any
	}{{"函数个数估算", &want.FleetSizes, got.FleetSizes}, {"并发与熔断推导", &want.FetchLimits, got.FetchLimits}, {"数据集", &want.Datasets, got.Datasets}, {"函数与请求", &want.Functions, got.Functions}}
	update := os.Getenv("MOOX_UPDATE_GOLDEN") == "1"
	for _, section := range sections {
		gotRaw, err := json.Marshal(section.got)
		require.NoError(t, err)
		if update {
			*section.want = gotRaw
			continue
		}
		// 记录文件里这一段的原文去掉空白后，必须与这边编码出来的字节相同。
		var wantRaw bytes.Buffer
		require.NoError(t, json.Compact(&wantRaw, *section.want))
		require.Equal(t, wantRaw.String(), string(gotRaw), "%s与改造前记录的不一致", section.name)
	}
	if update {
		// 只重写自己的四段，执行面的两段保持原样。
		raw, err := json.MarshalIndent(want, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(planRouteBaselineFile, append(raw, '\n'), 0o644))
		return
	}
	// 抽查一项，防止记录文件本身被改坏。
	require.Equal(t, 38, got.FleetSizes["5m/300"])
}
