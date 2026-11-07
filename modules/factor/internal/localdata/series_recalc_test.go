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

// presetSwapRanks 模拟实时路径已经为这些分钟写好的永续排名（有效值）。
func (fx *fixture) presetSwapRanks(minutes ...int) {
	for _, m := range minutes {
		for i, s := range []string{"BTC", "ETH"} {
			fx.st.mu.Lock()
			fx.st.results[s+at(m).Format(time.RFC3339)+swapTag] = storageio.ResultRow{SubjectID: s, DataTime: at(m), SeriesTag: swapTag, Fields: map[string]any{"rank": 0.25 + 0.5*float64(i)}}
			fx.st.mu.Unlock()
		}
	}
}

// 下架不久的合约（SOL 永续只有 0、1 两根，远没到下架的沉默阈值 6 小时）分不清是晚到还是下架，兜底推导仍把它当期望序列：
// 严格全集的截面因子在永续上因此缺数，永续 tag 被跳过——但补算类计划里这种推测缺数不能把 BTC、ETH 永续实时算好的有效排名覆盖成 NULL：
// 保留已有的值，现货照常排名。
func TestRecentlyDelistedSwapDoesNotOverwriteExistingRanksWithNull(t *testing.T) {
	fx := unifiedStoreFixtureWith(t, []int{0, 1}, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	fx.presetSwapRanks(7, 8, 9)
	// 补全 0~9 的本地历史（Prepare 只拉回看根数），让第 7~9 分钟都有完整的回看窗口。
	for _, s := range subjects {
		fx.putLocalEarly(t, s, spotTag, minutesBetween(0, 9)...)
	}
	for _, s := range []string{"BTC", "ETH"} {
		fx.putLocalEarly(t, s, swapTag, minutesBetween(0, 9)...)
	}
	series, err := fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, Expected: subjects, TargetStart: at(5), TargetEnd: at(10)})
	require.NoError(t, err)
	require.Len(t, series, 6, "SOL 永续沉默才 8 分钟，不到阈值：仍是期望序列")

	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
	}
	require.NoError(t, executor.Run(context.Background(), recalcexec.Window{Start: at(5), End: at(10)}, source,
		func(context.Context, time.Time, pipeline.Outcome) (bool, error) { return false, nil }))
	for _, m := range []int{7, 8, 9} {
		for i, s := range []string{"BTC", "ETH"} {
			swap, ok := fx.rawResult(s, swapTag, m)
			require.True(t, ok, s)
			require.Equal(t, 0.25+0.5*float64(i), swap.rank, "永续的有效排名保留：%s 第 %d 分钟", s, m)
		}
		for _, s := range subjects {
			spot, ok := fx.rawResult(s, spotTag, m)
			require.True(t, ok, s)
			require.NotNil(t, spot.rank, "现货照常排名：%s 第 %d 分钟", s, m)
		}
	}
}

// 已经沉默超过阈值的序列（下架的合约）不算期望序列：参照点是数据源最新一根，不是区间起点。历史块能排除早已下架的合约，不影响其他标的。
func TestLongDelistedSwapSeriesIsNotExpected(t *testing.T) {
	fx := unifiedStoreFixtureWith(t, []int{0, 1}, strictRank())
	// 数据源一直走到第 400 分钟；SOL 的永续停在第 1 分钟（沉默 399 分钟 > 6 小时）。
	for _, s := range []string{"BTC", "ETH"} {
		fx.putLocalEarly(t, s, swapTag, minutesBetween(10, 400)...)
	}
	for _, s := range []string{"BTC", "ETH", "SOL"} {
		fx.putLocalEarly(t, s, spotTag, minutesBetween(10, 400)...)
	}
	subjects := []string{"BTC", "ETH", "SOL"}
	series, err := fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, Expected: subjects, TargetStart: at(395), TargetEnd: at(396)})
	require.NoError(t, err)
	require.Len(t, series, 5)
	for _, item := range series {
		require.False(t, item.SubjectID == "SOL" && item.SeriesTag == swapTag)
	}
}

// 沉默阈值与周期、give_up_after 的关系：max(6 小时, 2×周期)，步长不小于 8 小时按 3 期；晚到一根的序列在任何周期下都不会被当成下架；恰等于阈值仍算期望。
func TestDelistedThresholdScalesWithPeriod(t *testing.T) {
	require.Equal(t, 6*time.Hour, DelistedThreshold(time.Minute))
	require.Equal(t, 6*time.Hour, DelistedThreshold(5*time.Minute))
	require.Equal(t, 6*time.Hour, DelistedThreshold(time.Hour), "1h 任务不随 give_up_after 变")
	require.Equal(t, 8*time.Hour, DelistedThreshold(4*time.Hour), "k×周期 大于固定时长时取 2 期")
	require.Equal(t, 72*time.Hour, DelistedThreshold(24*time.Hour), "日线按 3 期")
	require.Equal(t, 3*7*24*time.Hour, DelistedThreshold(7*24*time.Hour))

	start := t0.Add(100 * time.Hour)
	for _, step := range []time.Duration{time.Minute, time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		// 参照时刻取得离区间起点不远，让“阈值前的最后一行”仍落在区间起点之前。
		reference := start.Add(DelistedThreshold(step) / 2)
		// 晚到一根：最后一行是参照时刻的前一期。
		require.False(t, seriesDelisted(reference.Add(-step), start, reference, step), "步长 %s 晚到一根不是下架", step)
		// 恰等于阈值仍算期望，超过一点才是下架。
		edge := reference.Add(-DelistedThreshold(step))
		require.False(t, seriesDelisted(edge, start, reference, step), "步长 %s 沉默恰等于阈值仍算期望", step)
		require.True(t, seriesDelisted(edge.Add(-time.Second), start, reference, step), "步长 %s 超过阈值才是下架", step)
		// 延续到区间里的序列无论沉默多久都不算下架（区间内断档仍算缺失）。
		require.False(t, seriesDelisted(start.Add(time.Hour), start, reference.Add(1000*time.Hour), step))
	}
}

// 1d 任务永续晚到一根：兜底推导仍把永续当期望序列（沉默 1 天 < 3 期），所以只填充兜底恢复要 failed，不能因为现货行写成就确认；
// 人工补算（推测期望）里严格截面因子在永续上不写 NULL。
func TestDailyTaskLateSwapIsStillExpected(t *testing.T) {
	day := 24 * time.Hour
	base := t0
	all := []seriesBounds{
		{subject: "BTC", tag: spotTag, bounds: seriesstore.Bounds{Earliest: base, Latest: base.Add(10 * day)}},
		{subject: "BTC", tag: swapTag, bounds: seriesstore.Bounds{Earliest: base, Latest: base.Add(9 * day)}},
		{subject: "SOL", tag: swapTag, bounds: seriesstore.Bounds{Earliest: base, Latest: base.Add(8 * day)}},
	}
	got := expectedFromBounds(all, base.Add(10*day), base.Add(11*day), day)
	require.Equal(t, []seriesset.Series{{SubjectID: "BTC", SeriesTag: spotTag}, {SubjectID: "BTC", SeriesTag: swapTag}, {SubjectID: "SOL", SeriesTag: swapTag}}, got,
		"晚到一根、停更 2 天（沉默不超过 72 小时）都不是下架")
	delisted := append(all, seriesBounds{subject: "OLD", tag: swapTag, bounds: seriesstore.Bounds{Earliest: base, Latest: base.Add(2 * day)}})
	got = expectedFromBounds(delisted, base.Add(10*day), base.Add(11*day), day)
	require.NotContains(t, got, seriesset.Series{SubjectID: "OLD", SeriesTag: swapTag}, "沉默 8 天 > 72 小时")
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

func ratioFactor() domain.FactorDef {
	return domain.MustDefinition("ratio", "ratio", "output: close / volume\n")
}

// localHistory 先 Prepare（建好本地 schema），再直接往本地存储写各条序列的完整历史（不受 Prepare 只拉回看根数的限制）。
func localHistory(t *testing.T, fx *fixture, series map[string][]int) {
	t.Helper()
	fx.st.marketType = seriesset.MarketMulti
	fx.st.putTag("BTC", spotTag, 100, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	for tag, minutes := range series {
		fx.putLocalEarly(t, "BTC", tag, minutes...)
	}
}

func minutesBetween(from, to int) []int {
	var out []int
	for m := from; m <= to; m++ {
		out = append(out, m)
	}
	return out
}

func btcSeries(tags ...string) []seriesset.Series {
	var out []seriesset.Series
	for _, tag := range tags {
		out = append(out, seriesset.Series{SubjectID: "BTC", SeriesTag: tag})
	}
	return out
}

// fillItems 对 BTC 在 [start, end) 做只填充恢复，返回每个周期的逐项结果；frozen 为空表示没有冻结全集。
func fillItems(t *testing.T, fx *fixture, member string, start, end int, frozen []seriesset.Series) map[int]pipeline.ItemStatus {
	t.Helper()
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(start), TargetEnd: at(end),
		Expected: []string{"BTC"}, ExpectedSeries: frozen, Available: []string{"BTC"}, CarryColumns: []string{"close", "volume"}, FillOnly: true}
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	out := map[int]pipeline.ItemStatus{}
	for m := start; m < end; m++ {
		out[m] = outcome.Items[pipeline.ItemKey{Member: member, Subject: "BTC", Period: at(m)}]
	}
	return out
}

// 回看 1 根的因子：现货 0~9、永续 0~8，只填充恢复第 9 分钟。冻结全集里有永续（该期永续应当存在，只是晚到），永续缺第 9 根：必须 failed，
// 不能因为现货行写成就确认。没有冻结全集时（兜底推导）下架判断用固定时长，停更 1 分钟的永续同样是期望序列，也是 failed。
func TestFillOnlyLookbackOneFailsWhenSwapIsLate(t *testing.T) {
	for name, frozen := range map[string][]seriesset.Series{"冻结全集": btcSeries(spotTag, swapTag), "本地推导": nil} {
		fx := newFixture(t, ratioFactor())
		localHistory(t, fx, map[string][]int{spotTag: minutesBetween(0, 9), swapTag: minutesBetween(0, 8)})
		require.Equal(t, pipeline.ItemFailed, fillItems(t, fx, "ratio", 9, 10, frozen)[9], name)
	}
	// 永续第 9 根也到了：写成。
	fx := newFixture(t, ratioFactor())
	localHistory(t, fx, map[string][]int{spotTag: minutesBetween(0, 9), swapTag: minutesBetween(0, 9)})
	require.Equal(t, pipeline.ItemWritten, fillItems(t, fx, "ratio", 9, 10, btcSeries(spotTag, swapTag))[9])
}

// 回看 3 根、永续停更（本地 0~5，已超过回看）：恢复区间起点后移到 8、9 时，冻结全集里有永续就必须 failed。
func TestFillOnlyOutageLongerThanLookbackFailsWithFrozenUniverse(t *testing.T) {
	fx := newFixture(t, mean3())
	localHistory(t, fx, map[string][]int{spotTag: minutesBetween(0, 12), swapTag: minutesBetween(0, 5)})
	for _, start := range []int{6, 8, 9} {
		items := fillItems(t, fx, "mean", start, 10, btcSeries(spotTag, swapTag))
		for m := start; m < 10; m++ {
			require.Equal(t, pipeline.ItemFailed, items[m], "恢复区间起点 %d，第 %d 分钟", start, m)
		}
	}
}

// 永续在回看期内下架（本地 0~7）：冻结全集里不再有永续的那一期，现货的待办写成、确认，不会连跑多轮 failed 不收敛。
func TestFillOnlyConvergesAfterSwapDelistedWithinLookback(t *testing.T) {
	fx := newFixture(t, mean3())
	localHistory(t, fx, map[string][]int{spotTag: minutesBetween(0, 12), swapTag: minutesBetween(0, 7)})
	for round := 0; round < 3; round++ {
		require.Equal(t, pipeline.ItemWritten, fillItems(t, fx, "mean", 10, 11, btcSeries(spotTag))[10], "第 %d 轮", round)
	}
	// 下架之前的那一期，冻结全集里还有永续：它有行，写成。
	require.Equal(t, pipeline.ItemWritten, fillItems(t, fx, "mean", 7, 8, btcSeries(spotTag, swapTag))[7])
}

// 失效重算的生产入口（engine/series.go 的 seriesFollower.run）构造的 Selection 不带 Series：期望序列由数据层兜底推出。
// SOL 的永续停更（本地只有 0、1），实时路径这一期写的是 NULL；重算时 SOL 永续仍是期望序列（停更远没到下架的固定时长），
// 永续 tag 的严格截面因子整体跳过，不会在 BTC、ETH 上写出残缺排名（0.5、1）覆盖 NULL。
func TestInvalidationProductionPathDoesNotOverwriteWithPartialUniverse(t *testing.T) {
	fx := unifiedStoreFixture(t, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	fx.presetSwapRanks(9)
	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) { // 与 seriesFollower.run 一致：只给标的，不给 Series
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
	}
	_, err := executor.RunChunkWith(context.Background(), source, at(9), at(10), nil)
	require.NoError(t, err)
	for i, s := range []string{"BTC", "ETH"} {
		swap, ok := fx.rawResult(s, swapTag, 9)
		require.True(t, ok)
		require.Equal(t, 0.25+0.5*float64(i), swap.rank, "重算后永续已有的排名保留，不在 BTC、ETH 上写出残缺排名也不写 NULL：%s", s)
	}
	for _, s := range subjects {
		spot, ok := fx.rawResult(s, spotTag, 9)
		require.True(t, ok, s)
		require.NotNil(t, spot.rank, "现货照常排名：%s", s)
	}
}

// 单序列数据集：没有 ExpectedSeries 的计划不受影响，缺目标那一根仍是 failed，与统一数据集改造之前一致。
func TestFillOnlySingleSeriesUnchanged(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	require.Equal(t, pipeline.ItemFailed, fillItems(t, fx, "mean", 9, 10, nil)[9])
}
