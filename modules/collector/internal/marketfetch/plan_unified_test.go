package marketfetch

import (
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// unifiedGroups 是同一个数据集挂现货、永续两个标签（统一数据集）：每个市场一组，标的不完全相同。
func unifiedGroups(spot, swap []string) []TaskGroup {
	return []TaskGroup{
		{Provider: "binance", SourceID: "spot_http", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot",
			DatasetID: "ds_unified", Frequency: "1m", Subjects: spot},
		{Provider: "binance", SourceID: "swap_http", MarketType: "swap", MarketID: "crypto", InstrumentType: "perpetual",
			DatasetID: "ds_unified", Frequency: "1m", Subjects: swap},
	}
}

func subjectNames(prefix string, count int) []string {
	out := make([]string, count)
	for i := range out {
		out[i] = fmt.Sprintf("%s%03d-USDT", prefix, i)
	}
	return out
}

// 同一个数据集来自两个市场：两个市场的函数都包含这个数据集，共用同一份快照哈希，序号覆盖两条序列且互不重叠。
func TestBuildDesiredStateUnifiedDataset(t *testing.T) {
	options := testPlanOptions()
	options.MarketLatenessS = map[string]int{"spot": 15, "swap": 20}
	spot := subjectNames("S", 3)
	swap := append(subjectNames("S", 2), "W000-USDT") // 永续少一个现货标的，多一个只有永续的标的
	state, err := BuildDesiredState(options, unifiedGroups(spot, swap), nil)
	require.NoError(t, err)

	dataset := state.Datasets["ds_unified|1m"]
	require.NotNil(t, dataset)
	require.Equal(t, uint32(6), dataset.Count, "3 个现货序列加 3 个永续序列")
	require.Equal(t, 20, dataset.LatenessS, "允许迟到取两个市场里较大的")
	spotTag := PlanSeriesTag("crypto", PlanMarket{Provider: "binance", SourceID: "spot_http", MarketType: "spot"})
	swapTag := PlanSeriesTag("crypto", PlanMarket{Provider: "binance", SourceID: "swap_http", MarketType: "swap"})
	require.Equal(t, "venue:binance|market:spot|source:spot_http", spotTag)
	require.Equal(t, "venue:binance|market:swap|source:swap_http", swapTag)

	// 哈希与共享包对同一批（标的、tag）算出的一致。
	var series []seriesset.Series
	for _, subject := range spot {
		series = append(series, seriesset.Series{SubjectID: subject, SeriesTag: spotTag})
	}
	for _, subject := range swap {
		series = append(series, seriesset.Series{SubjectID: subject, SeriesTag: swapTag})
	}
	set, err := seriesset.Canonicalize(series)
	require.NoError(t, err)
	require.Equal(t, set.Hash, dataset.Hash)

	covered := map[uint32]string{}
	for name, function := range state.Functions {
		planDataset := findPlanDataset(function.Plan, "ds_unified|1m")
		require.NotNil(t, planDataset, "两个市场的函数都要包含这个数据集：%s", name)
		require.Equal(t, set.Hash, planDataset.Copies[0].Hash, "共用同一份快照哈希")
		require.Equal(t, uint32(6), planDataset.Copies[0].Count)
		tag := PlanSeriesTag("crypto", function.Plan.Market)
		for _, item := range planDataset.Copies[0].Subjects {
			require.Equal(t, set.Series[item.Index].SubjectID, item.ID)
			require.Equal(t, tag, set.Series[item.Index].SeriesTag, "每个市场只提交自己 tag 的序号")
			require.NotContains(t, covered, item.Index, "两个市场的序号不重叠")
			covered[item.Index] = name
		}
	}
	require.Len(t, covered, 6, "两个市场合起来正好覆盖全部序列")

	// 函数名只含市场类型，两个市场各自的函数分开；SCF 函数照常能编码与严格解码计划。
	for _, function := range state.Functions {
		for i := range function.Plan.Datasets {
			function.Plan.Datasets[i].Copies[0].From = 100 // 生效起点由计划内容生成时填入
		}
		encoded, err := EncodePlan("crypto", function.Plan)
		require.NoError(t, err)
		decoded, err := DecodePlan(encoded)
		require.NoError(t, err)
		require.Equal(t, function.Plan.Market, decoded.Market)
		require.Equal(t, function.Plan.Datasets[0].Copies, decoded.Datasets[0].Copies)
	}
}

// 单市场数据集：计划（序号、哈希、份）与统一改造之前一致，不带任何新字段。
func TestBuildDesiredStateSingleMarketUnchangedByUnifiedSupport(t *testing.T) {
	state, err := BuildDesiredState(testPlanOptions(), testGroups(3, "1m"), nil)
	require.NoError(t, err)
	dataset := state.Datasets["ds_spot_1m|1m"]
	tag := PlanSeriesTag("crypto", PlanMarket{Provider: "binance", SourceID: "binance", MarketType: "spot"})
	require.Equal(t, tag, dataset.Series[0].SeriesTag)
	require.Equal(t, uint32(3), dataset.Count)
	for _, function := range state.Functions {
		planDataset := findPlanDataset(function.Plan, "ds_spot_1m|1m")
		require.Equal(t, []PlanSubject{{ID: "S000-USDT", Index: 0}, {ID: "S001-USDT", Index: 1}, {ID: "S002-USDT", Index: 2}}, planDataset.Copies[0].Subjects)
	}
}

// 新标的只加进永续（现货里本来就有）：变更里的新增要列出“标的@市场”，不能因为标的名在现货里已有而漏掉。
func TestBuildPlanContentListsAddedSeriesOfUnifiedDataset(t *testing.T) {
	options := testPlanOptions()
	first, err := BuildDesiredState(options, unifiedGroups(subjectNames("S", 3), subjectNames("S", 2)), nil)
	require.NoError(t, err)
	previousDatasets := map[string]*PlanDatasetState{}
	for key, dataset := range first.Datasets {
		state := dataset.PlanDatasetState
		previousDatasets[key] = &state
	}
	for _, function := range first.Functions {
		for i := range function.Plan.Datasets {
			function.Plan.Datasets[i].Copies[0].From = 100
		}
	}
	second, err := BuildDesiredState(options, unifiedGroups(subjectNames("S", 3), subjectNames("S", 3)), first.Functions)
	require.NoError(t, err)
	content, changed, err := BuildPlanContent(first.Functions, previousDatasets, second, func(string) (int64, error) { return 200, nil })
	require.NoError(t, err)
	require.True(t, changed)
	change := content.Datasets["ds_unified|1m"]
	require.Equal(t, []string{"S002-USDT@swap"}, change.Added)
	require.Empty(t, change.Removed)
}
