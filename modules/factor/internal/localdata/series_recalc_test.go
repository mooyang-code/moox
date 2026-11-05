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
	"github.com/stretchr/testify/require"
)

func strictRank() domain.FactorDef {
	return domain.MustDefinition("rank", "rank", "output: cs_rank(ts_mean(close, 3))\n")
}

// unifiedStoreFixture 造统一数据集的本地数据：现货三个币都在；永续 BTC、ETH 在，SOL 的永续序列只有开头两根（序列存在，但整个计算区间都没有数据）。
func unifiedStoreFixture(t *testing.T, factors ...domain.FactorDef) *fixture {
	t.Helper()
	fx := newFixture(t, factors...)
	// 各标的的价格不同，截面排名才有区分。
	for i, s := range []string{"BTC", "ETH", "SOL"} {
		fx.st.putTag(s, spotTag, float64(100+10*i), 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}
	for i, s := range []string{"BTC", "ETH"} {
		fx.st.putTag(s, swapTag, float64(200+10*i), 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH", "SOL"}))
	fx.putLocalEarly(t, "SOL", swapTag, 0, 1)
	return fx
}

// putLocalEarly 直接往本地存储写某条序列开头的几根：序列存在，但在后面的计算区间（含回看）里没有数据。
func (fx *fixture) putLocalEarly(t *testing.T, subject, tag string, minutes ...int) {
	t.Helper()
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields, ok, err := fx.data.Store().Schema(src)
	require.NoError(t, err)
	require.True(t, ok)
	rows := seriesstore.NewColumns(fields, len(minutes))
	for _, m := range minutes {
		values := make([]seriesstore.Value, len(fields))
		for i := range values {
			values[i] = seriesstore.Float(200 + float64(m))
		}
		rows.AppendRow(at(m), tag, false, values...)
	}
	_, err = fx.data.Store().WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: subject, From: at(minutes[0]), To: at(minutes[len(minutes)-1] + 1), Rows: rows, Scoped: true}, seriesstore.WriteOptions{})
	require.NoError(t, err)
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

// 人工补算（executor.Run）：统一数据集上永续缺 SOL 时，严格全集的截面因子只跳过永续（行写 NULL，与实时路径一致），现货照常在三个币之间排名。
// 补算拿不到周期 marker，期望序列由本地存储里“应存在”的（标的、tag）构造。
func TestManualRecalcOnUnifiedDatasetSkipsOnlyTheSwapMarket(t *testing.T) {
	fx := unifiedStoreFixture(t, strictRank())
	subjects := []string{"BTC", "ETH", "SOL"}
	series, err := fx.data.ExpectedSeries(context.Background(), pipeline.Plan{Set: fx.set.Set, Expected: subjects, TargetEnd: at(10)})
	require.NoError(t, err)
	require.Len(t, series, 6, "SOL 的永续序列在区间结束前已有数据，应存在")

	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
	}
	err = executor.Run(context.Background(), recalcexec.Window{Start: at(9), End: at(10)}, source,
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
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
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

// 时序因子的空洞恢复（只填充）：永续在整个区间都没数据时，现货写成功不能把 (成员, 标的, 周期) 确认掉——这一项要等永续补上。
func TestFillOnlyRecoveryDoesNotConfirmWhileSwapSeriesMissing(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.putTag("BTC", spotTag, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	fx.putLocalEarly(t, "BTC", swapTag, 0, 1)

	fill := func() pipeline.Outcome {
		plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(9), TargetEnd: at(10),
			Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close", "volume"}, FillOnly: true}
		outcome, err := fx.runner.Run(context.Background(), plan)
		require.NoError(t, err)
		return outcome
	}
	key := pipeline.ItemKey{Member: "mean", Subject: "BTC", Period: at(9)}
	outcome := fill()
	require.Equal(t, pipeline.ItemFailed, outcome.Items[key], "永续整个区间没有数据：这一项不算补齐，即使现货这一期是好的")

	// 对照：永续在区间内有数据时，同一项写成，确认。
	full := newFixture(t, mean3())
	full.st.putTag("BTC", spotTag, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	full.st.putTag("BTC", swapTag, 200, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, full.data.Prepare(context.Background(), full.set, []string{"BTC"}))
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: full.set.Set, Members: full.set.Members, TargetStart: at(9), TargetEnd: at(10),
		Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close", "volume"}, FillOnly: true}
	outcome, err := full.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, pipeline.ItemWritten, outcome.Items[key])
}

// 单序列数据集（序列不跨现货、永续）不构造期望序列：计划保持按标的处理，与引入序列之前逐位一致。
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
