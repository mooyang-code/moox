package localdata

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

func strictRank() domain.FactorDef {
	return domain.MustDefinition("rank", "rank", "output: cs_rank(ts_mean(close, 3))\n")
}

// unifiedStoreFixture 造统一数据集（数据集属性 market_type=multi）的本地数据：现货三个币都在；永续 BTC、ETH 在，
// SOL 的永续序列只有 solSwapMinutes 这几根（默认 0、1：在计算区间的读取起点之前就结束了）。
func unifiedStoreFixture(t *testing.T, factors ...domain.FactorDef) *fixture {
	t.Helper()
	return unifiedStoreFixtureWith(t, []int{0, 1}, factors...)
}

func unifiedStoreFixtureWith(t *testing.T, solSwapMinutes []int, factors ...domain.FactorDef) *fixture {
	t.Helper()
	fx := newFixture(t, factors...)
	fx.st.marketType = seriesset.MarketMulti
	// 各标的的价格不同，截面排名才有区分。
	for i, s := range []string{"BTC", "ETH", "SOL"} {
		fx.st.putTag(s, spotTag, float64(100+10*i), 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}
	for i, s := range []string{"BTC", "ETH"} {
		fx.st.putTag(s, swapTag, float64(200+10*i), 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH", "SOL"}))
	fx.putLocalEarly(t, "SOL", swapTag, solSwapMinutes...)
	return fx
}

// putLocalEarly 直接往本地存储写某条序列的几根（分钟序号），中间可以断档。
func (fx *fixture) putLocalEarly(t *testing.T, subject, tag string, minutes ...int) {
	t.Helper()
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields, ok, err := fx.data.Store().Schema(src)
	require.NoError(t, err)
	require.True(t, ok)
	// 连续的分钟一次写；断档处分开写（本地写入不允许一批里有缺期）。
	for start := 0; start < len(minutes); {
		end := start + 1
		for end < len(minutes) && minutes[end] == minutes[end-1]+1 {
			end++
		}
		run := minutes[start:end]
		rows := seriesstore.NewColumns(fields, len(run))
		for _, m := range run {
			values := make([]seriesstore.Value, len(fields))
			for i := range values {
				values[i] = seriesstore.Float(200 + float64(m))
			}
			rows.AppendRow(at(m), tag, false, values...)
		}
		_, err = fx.data.Store().WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: subject, From: at(run[0]), To: at(run[len(run)-1] + 1), Rows: rows, Scoped: true}, seriesstore.WriteOptions{})
		require.NoError(t, err)
		start = end
	}
}

func (fx *fixture) rawResult(subject, tag string, m int) (storageResult, bool) {
	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	row, ok := fx.st.results[subject+at(m).Format(time.RFC3339)+tag]
	if !ok {
		return storageResult{}, false
	}
	return storageResult{rank: row.Fields["rank"], mean: row.Fields["mean"]}, true
}

type storageResult struct{ rank, mean any }

// 冻结全集（空洞恢复读 marker 里的 universe_series）里有 SOL 的永续、本地存储里它只有开头两根：严格全集的截面因子只跳过永续
// （行写 NULL，与实时路径一致），现货照常在三个币之间排名。
func TestFrozenUniverseSkipsOnlyTheSwapMarketWhenASeriesIsMissing(t *testing.T) {
	fx := unifiedStoreFixture(t, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Series: unifiedUniverse(subjects...), Columns: []string{"close", "volume"}}, nil
	}
	err := executor.Run(context.Background(), recalcexec.Window{Start: at(9), End: at(10)}, source,
		func(context.Context, time.Time, pipeline.Outcome) (bool, error) { return false, nil })
	require.NoError(t, err)

	for _, s := range subjects {
		spot, ok := fx.rawResult(s, spotTag, 9)
		require.True(t, ok, s)
		require.NotNil(t, spot.rank, "现货照常排名：%s", s)
	}
	for _, s := range []string{"BTC", "ETH"} {
		swap, ok := fx.rawResult(s, swapTag, 9)
		require.True(t, ok, s)
		require.Nil(t, swap.rank, "永续缺 SOL，严格全集的截面因子跳过：写 NULL，不在 BTC、ETH 两个币之间算出残缺排名：%s", s)
	}
}

// 人工补算（executor.Run）拿不到周期 marker，期望序列由本地存储里“应存在”的（标的、tag）构造：SOL 的永续序列延续到区间内（只缺目标那一根）时是期望序列，
// 现货照常排名，SOL 永续这一期没有行、不写；它在区间前就结束时不是期望序列（见 TestDelistedSwapSeriesIsNotExpected）。
func TestManualRecalcBuildsExpectedSeriesFromLocalStore(t *testing.T) {
	fx := unifiedStoreFixtureWith(t, []int{0, 1, 2, 3, 4, 5, 6, 7, 8}, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	series, err := fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, Expected: subjects, TargetStart: at(9), TargetEnd: at(10)})
	require.NoError(t, err)
	require.Len(t, series, 6, "SOL 的永续序列延续到读取范围内，应存在")

	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
	}
	require.NoError(t, executor.Run(context.Background(), recalcexec.Window{Start: at(9), End: at(10)}, source,
		func(context.Context, time.Time, pipeline.Outcome) (bool, error) { return false, nil }))
	for _, s := range subjects {
		spot, ok := fx.rawResult(s, spotTag, 9)
		require.True(t, ok, s)
		require.NotNil(t, spot.rank, "现货照常排名：%s", s)
	}
	_, ok := fx.rawResult("SOL", swapTag, 9)
	require.False(t, ok, "SOL 永续这一期没有源行，不写")
}

// 失效重算（executor.RunChunkWith，非只填充）与人工补算同一条路径：同样不会在残缺的全集上算出结果覆盖实时路径写的 NULL。
func TestInvalidationRecomputeOnUnifiedDatasetDoesNotOverwriteWithPartialUniverse(t *testing.T) {
	fx := unifiedStoreFixture(t, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	// 实时路径已经为这一期写过：永续的行是 NULL。
	for _, s := range []string{"BTC", "ETH"} {
		fx.st.mu.Lock()
		fx.st.results[s+at(9).Format(time.RFC3339)+swapTag] = storageio.ResultRow{SubjectID: s, DataTime: at(9), SeriesTag: swapTag, Fields: map[string]any{"rank": nil}}
		fx.st.mu.Unlock()
	}
	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Series: unifiedUniverse(subjects...), Columns: []string{"close", "volume"}}, nil
	}
	_, err := executor.RunChunkWith(context.Background(), source, at(9), at(10), nil)
	require.NoError(t, err)
	for _, s := range []string{"BTC", "ETH"} {
		swap, ok := fx.rawResult(s, swapTag, 9)
		require.True(t, ok)
		require.Nil(t, swap.rank, "重算后永续仍是 NULL：%s", s)
	}
	spot, ok := fx.rawResult("SOL", spotTag, 9)
	require.True(t, ok)
	require.NotNil(t, spot.rank)
}

// 单序列数据集（market_type 不是 multi）不构造期望序列：计划保持按标的处理，与引入序列之前逐位一致；
// 即使本地存储里碰巧有跨市场的 tag 也不按统一数据集处理（以数据集属性为准）。
func TestExpectedSeriesIsNilForSingleSeriesDatasets(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.putTag("ETH", spotTag, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH"}))
	series, err := fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Set: fx.set.Set, Expected: []string{"BTC", "ETH"}, TargetEnd: at(10)})
	require.NoError(t, err)
	require.Nil(t, series)

	// 同一市场下有两条不同的 tag（例如导入时用错了 tag）也不是统一数据集。
	fx.st.putTag("ETH", "venue:binance|market:spot|source:other", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH"}))
	series, err = fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Set: fx.set.Set, Expected: []string{"BTC", "ETH"}, TargetEnd: at(10)})
	require.NoError(t, err)
	require.Nil(t, series)
}

// 区间前结束的序列（下架的合约）不算期望序列：不影响其他标的，严格全集的截面因子在永续上照常按 BTC、ETH 排名，
// 不会因为下架的 SOL 永续把整个永续 tag 跳过、把实时算好的排名覆盖成 NULL。
func TestDelistedSwapSeriesIsNotExpected(t *testing.T) {
	fx := unifiedStoreFixtureWith(t, []int{0, 1}, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	series, err := fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, Expected: subjects, TargetStart: at(9), TargetEnd: at(10)})
	require.NoError(t, err)
	require.Len(t, series, 5, "SOL 的永续序列在读取起点之前就结束了：不算期望")
	for _, item := range series {
		require.False(t, item.SubjectID == "SOL" && item.SeriesTag == swapTag)
	}

	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
	}
	require.NoError(t, executor.Run(context.Background(), recalcexec.Window{Start: at(9), End: at(10)}, source,
		func(context.Context, time.Time, pipeline.Outcome) (bool, error) { return false, nil }))
	for _, s := range []string{"BTC", "ETH"} {
		swap, ok := fx.rawResult(s, swapTag, 9)
		require.True(t, ok, s)
		require.NotNil(t, swap.rank, "永续照常排名：%s", s)
	}
}

// 永续这一期晚到（现货 0~9 分钟、永续 0~8 分钟），对第 9 分钟只填充恢复时，现货行写成不能把 (成员, 标的, 周期) 确认掉；
// 单序列缺同一根时是 failed，多序列同口径。
func TestFillOnlyRecoveryFailsWhenOneSeriesLacksTheTargetBar(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.marketType = seriesset.MarketMulti
	fx.st.putTag("BTC", spotTag, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.putTag("BTC", swapTag, 200, 0, 1, 2, 3, 4, 5, 6, 7, 8)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(9), TargetEnd: at(10),
		Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close", "volume"}, FillOnly: true}
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	key := pipeline.ItemKey{Member: "mean", Subject: "BTC", Period: at(9)}
	require.Equal(t, pipeline.ItemFailed, outcome.Items[key], "永续缺第 9 根：不算补齐")

	// 对照：永续第 9 根也到了，同一项写成，确认。
	full := newFixture(t, mean3())
	full.st.marketType = seriesset.MarketMulti
	full.st.putTag("BTC", spotTag, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	full.st.putTag("BTC", swapTag, 200, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, full.data.Prepare(context.Background(), full.set, []string{"BTC"}))
	fullPlan := plan
	fullPlan.Set, fullPlan.Members = full.set.Set, full.set.Members
	outcome, err = full.runner.Run(context.Background(), fullPlan)
	require.NoError(t, err)
	require.Equal(t, pipeline.ItemWritten, outcome.Items[key])

	// 单序列回归：缺第 9 根同样是 failed。
	single := newFixture(t, mean3())
	single.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8)
	require.NoError(t, single.data.Prepare(context.Background(), single.set, []string{"BTC"}))
	plan.Set, plan.Members = single.set.Set, single.set.Members
	outcome, err = single.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, pipeline.ItemFailed, outcome.Items[key])
}
