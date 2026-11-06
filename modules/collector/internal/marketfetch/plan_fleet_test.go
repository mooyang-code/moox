package marketfetch

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFleetSizingFromSourceLimits(t *testing.T) {
	sizing := FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 1}
	sources := []SourceCapacity{
		{Provider: "tdx", Weight: 4, MaxConcurrent: 16, RPS: 60, Burst: 16},
		{Provider: "eastmoney", Weight: 2, MaxConcurrent: 16, RPS: 50, Burst: 20},
		{Provider: "sina", Weight: 1, MaxConcurrent: 16, RPS: 50, Burst: 20},
	}
	// 2 秒窗口、每请求 0.5 秒 → 每个并发槽位发 4 个请求；通达信容量 min(16×4, 16+60×2) = 64，占 4/7：64 × 7 ÷ 4 ÷ 1.2 = 93。
	require.Equal(t, 93, sizing.SymbolsPerFunction(sources, 300))
	// 标的再多也不改变每函数标的数，只增加函数个数。
	require.Equal(t, 93, sizing.SymbolsPerFunction(sources, 3000))

	// 最少函数个数：标的少时也要分开，每函数标的数取较小值。
	sizing.MinFunctions = 4
	require.Equal(t, 75, sizing.SymbolsPerFunction(sources, 300))
	require.Equal(t, 93, sizing.SymbolsPerFunction(sources, 3000))
	require.Equal(t, 1, sizing.SymbolsPerFunction(sources, 3), "不足 MinFunctions 只时每个函数 1 只")
}

func TestFleetSizingFollowsTheTightestSource(t *testing.T) {
	sizing := FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 1}
	loose := []SourceCapacity{{Provider: "a", Weight: 1, MaxConcurrent: 16, RPS: 100, Burst: 20}}
	tight := []SourceCapacity{{Provider: "a", Weight: 1, MaxConcurrent: 16, RPS: 5, Burst: 2}}
	require.Greater(t, sizing.SymbolsPerFunction(loose, 1000), sizing.SymbolsPerFunction(tight, 1000))
	// 频率上限更紧时按频率算：突发 2 + 每秒 5 × 2 秒 = 12 个请求，除以余量 1.2 = 10。
	require.Equal(t, 10, sizing.SymbolsPerFunction(tight, 1000))
	// 权重越高的源承担越多请求，限制越紧。
	heavy := []SourceCapacity{{Provider: "a", Weight: 9, MaxConcurrent: 8, RPS: 30, Burst: 10}, {Provider: "b", Weight: 1, MaxConcurrent: 8, RPS: 30, Burst: 10}}
	even := []SourceCapacity{{Provider: "a", Weight: 1, MaxConcurrent: 8, RPS: 30, Burst: 10}, {Provider: "b", Weight: 1, MaxConcurrent: 8, RPS: 30, Burst: 10}}
	require.Less(t, sizing.SymbolsPerFunction(heavy, 1000), sizing.SymbolsPerFunction(even, 1000))
}

func TestFleetSizingReturnsZeroWhenItCannotEstimate(t *testing.T) {
	sizing := FleetSizing{TargetFetchMS: 2000, RequestMS: 500}
	require.Zero(t, sizing.SymbolsPerFunction(nil, 300))
	require.Zero(t, sizing.SymbolsPerFunction([]SourceCapacity{{Provider: "a", Weight: 0, MaxConcurrent: 8}}, 300))
	require.Zero(t, sizing.SymbolsPerFunction([]SourceCapacity{{Provider: "a", Weight: 1, MaxConcurrent: 8}}, 0))
	require.Zero(t, FleetSizing{}.SymbolsPerFunction([]SourceCapacity{{Provider: "a", Weight: 1, MaxConcurrent: 8}}, 300))
}

func TestStockCNAutoSizeUsesTheReleaseRouteAndProviderLimits(t *testing.T) {
	t.Chdir("..")
	size := StockCNAutoSize(FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 4})
	got := size(TaskGroup{Frequency: "5m"}, 300)
	require.Positive(t, got)
	require.LessOrEqual(t, got, 75)
	require.Zero(t, size(TaskGroup{Frequency: "15m"}, 300), "没有这个频率的路由时算不出")
}

// 函数个数随标的数和限速配置自动调整：沪深 300 用 A 股的真实路由与源限速估算，标的翻倍函数个数增加，每个函数的标的数不变。
func TestBuildDesiredStateSizesTheStockCNFleetFromLimits(t *testing.T) {
	t.Chdir("..")
	options := func(autoSize func(TaskGroup, int) int) PlanOptions {
		return PlanOptions{
			SpaceID: "stockcn", Markets: map[string][]string{"equity": {"ap-nanjing"}}, SymbolsPerFunction: 50, DefaultLatenessS: 90,
			Fetch: PlanFetch{Concurrency: 50, RPS: 50, Burst: 50, FeedConcurrency: 50}, AutoSize: autoSize, DeriveFetch: StockCNFetchLimits(),
		}
	}
	groups := func(count int) []TaskGroup {
		subjects := make([]string, 0, count)
		for i := 0; i < count; i++ {
			subjects = append(subjects, fmt.Sprintf("%06d.XSHG", 600000+i))
		}
		return []TaskGroup{{Provider: "eastmoney", SourceID: "stockcn_http", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity",
			DatasetID: "ds_cn_5m", Frequency: "5m", Subjects: subjects}}
	}
	auto := StockCNAutoSize(FleetSizing{TargetFetchMS: 2000, RequestMS: 500, MinFunctions: 4})
	sizeOf := func(opts PlanOptions, count int) (int, int) {
		state, err := BuildDesiredState(opts, groups(count), nil)
		require.NoError(t, err)
		perFunction := 0
		for _, function := range state.Functions {
			for _, dataset := range function.Plan.Datasets {
				perFunction = max(perFunction, len(dataset.Copies[len(dataset.Copies)-1].Subjects))
			}
		}
		return len(state.Functions), perFunction
	}
	functions, per := sizeOf(options(auto), 300)
	require.Equal(t, 4, functions, "沪深 300：至少 4 个函数，每个函数 75 只")
	require.Equal(t, 75, per)
	state, err := BuildDesiredState(options(auto), groups(300), nil)
	require.NoError(t, err)
	for _, function := range state.Functions {
		require.Equal(t, 48, function.Plan.Fetch.Concurrency)
		require.Zero(t, function.Plan.Fetch.RPS)
	}
	functions, per = sizeOf(options(auto), 1000)
	require.Greater(t, functions, 4, "标的变多，函数个数随之增加")
	require.LessOrEqual(t, per, 93, "每个函数的标的数不超过按限速算出的上限")
	// 不估算时仍按固定的每函数标的数。
	functions, _ = sizeOf(options(nil), 300)
	require.Equal(t, 6, functions)
}

// 函数内并发取各主源并发之和，熔断并发取最大值，rps、burst 对 A 股没有作用而清零；读不出源配置时保持传入的值。
func TestStockCNFetchLimitsAreDerivedFromTheSourceLimits(t *testing.T) {
	t.Chdir("..")
	derive := StockCNFetchLimits()
	got := derive(TaskGroup{Frequency: "5m"}, PlanFetch{Concurrency: 50, RPS: 50, Burst: 50, FeedConcurrency: 50, RequestTimeoutMS: 3000})
	require.Equal(t, 48, got.Concurrency, "tdx 16 + eastmoney 16 + sina 16")
	require.Equal(t, 16, got.FeedConcurrency)
	require.Zero(t, got.RPS)
	require.Zero(t, got.Burst)
	require.Equal(t, 3000, got.RequestTimeoutMS, "其余参数不动")
	unchanged := PlanFetch{Concurrency: 7, RPS: 9, FeedConcurrency: 3}
	require.Equal(t, unchanged, derive(TaskGroup{Frequency: "15m"}, unchanged), "没有这个频率的路由时保持原值")
}
