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
		_, err = fx.data.Store().WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: subject, From: at(run[0]), To: at(run[len(run)-1] + 1), Rows: rows}, seriesstore.WriteOptions{})
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

// latest 返回标的本地数据的最新一根。
func (fx *fixture) latest(t *testing.T, subject string) time.Time {
	t.Helper()
	bounds, _, err := fx.data.cfg.Store.Bounds(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}, subject)
	require.NoError(t, err)
	return bounds.Latest
}

// 补算类计划的期望标的：本地没有数据的期望标的（SOL）照常保留，按缺数处理；本地完全没有数据时报 ErrNoLocalData。
func TestExpectedSubjectsKeepsSubjectsWithoutLocalData(t *testing.T) {
	fx := newFixture(t, mean3())
	_, err := fx.data.ExpectedSubjects(context.Background(), pipeline.Plan{Set: fx.set.Set, Expected: []string{"BTC"}, TargetEnd: at(10)})
	require.ErrorIs(t, err, ErrNoLocalData, "本地一个标的都没有：没有可补的数据，补算直接报错")

	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("ETH", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH"}))
	subjects, err := fx.data.ExpectedSubjects(context.Background(), pipeline.Plan{Set: fx.set.Set, Expected: []string{"BTC", "ETH", "SOL"}, TargetEnd: at(10)})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, subjects)
}

// 没有冻结全集的补算跨过 tag 切换：BTC 第 5 分钟起从 tag a 换到 tag b，补算 [3,8) 五期都按各自那一期的源行写成，
// 不再只认最新的 tag（改前 3、4 两期记成 failed）。
func TestRecalcAcrossTagSwitchWritesEveryPeriod(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, minutesBetween(0, 4)...)
	fx.st.putTag("BTC", "venue:b", 100, minutesBetween(5, 9)...)
	fx.now = at(10)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	// 同步只取最近的回看窗口：更早的几根直接写进本地存储（补算只读本地）。
	fx.putLocalEarly(t, "BTC", plainTag, minutesBetween(0, 4)...)
	fx.putLocalEarly(t, "BTC", "venue:b", minutesBetween(5, 9)...)
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(3), TargetEnd: at(8),
		Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close", "volume"}, WriteCarry: true}
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	for m := 3; m < 8; m++ {
		require.Equal(t, pipeline.ItemWritten, outcome.Items[pipeline.ItemKey{Member: "mean", Subject: "BTC", Period: at(m)}], "第 %d 分钟", m)
	}
	_, ok := fx.rawResult("BTC", plainTag, 4)
	require.True(t, ok, "旧 tag 那一期按旧 tag 的行键写出")
	_, ok = fx.rawResult("BTC", "venue:b", 5)
	require.True(t, ok, "新 tag 那一期按新 tag 的行键写出")
}

func minutesBetween(from, to int) []int {
	var out []int
	for m := from; m <= to; m++ {
		out = append(out, m)
	}
	return out
}

// fillItems 对 BTC 在 [start, end) 做只填充恢复（没有冻结全集），返回每个周期的逐项结果。
func fillItems(t *testing.T, fx *fixture, member string, start, end int) map[int]pipeline.ItemStatus {
	t.Helper()
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(start), TargetEnd: at(end),
		Expected: []string{"BTC"}, Available: []string{"BTC"}, CarryColumns: []string{"close", "volume"}, FillOnly: true}
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	out := map[int]pipeline.ItemStatus{}
	for m := start; m < end; m++ {
		out[m] = outcome.Items[pipeline.ItemKey{Member: member, Subject: "BTC", Period: at(m)}]
	}
	return out
}

// 没有冻结全集的只填充计划：缺目标那一根是 failed。
func TestFillOnlySingleSeriesUnchanged(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	require.Equal(t, pipeline.ItemFailed, fillItems(t, fx, "mean", 9, 10)[9])
}

// 补算上市前的区间：SOL 第 12 分钟才有本地数据，补算 [5,10) 时它还没上市，不按缺数处理；
// 否则严格截面因子整段跳过，BTC、ETH 已有的排名被写成 NULL（自动失效重算同样走这里）。
func TestSingleTagRecalcBeforeListingKeepsExistingRanks(t *testing.T) {
	fx := newFixture(t, strictRank())
	fx.st.put("BTC", 100, minutesBetween(0, 9)...)
	fx.st.put("ETH", 110, minutesBetween(0, 9)...)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC", "ETH"}))
	for _, s := range []string{"BTC", "ETH"} {
		fx.putLocalEarly(t, s, plainTag, minutesBetween(0, 9)...)
	}
	fx.putLocalEarly(t, "SOL", plainTag, 12, 13, 14)
	for _, m := range []int{7, 8, 9} {
		for i, s := range []string{"BTC", "ETH"} {
			fx.st.mu.Lock()
			fx.st.results[s+at(m).Format(time.RFC3339)+plainTag] = storageio.ResultRow{SubjectID: s, DataTime: at(m), SeriesTag: plainTag, Fields: map[string]any{"rank": 0.25 + 0.5*float64(i)}}
			fx.st.mu.Unlock()
		}
	}
	subjects := []string{"BTC", "ETH", "SOL"}
	expected, err := fx.data.ExpectedSubjects(context.Background(), pipeline.Plan{Set: fx.set.Set, Expected: subjects, TargetStart: at(5), TargetEnd: at(10)})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH"}, expected, "区间结束时还没上市的 SOL 不在期望标的里")

	executor := recalcexec.NewExecutor(fx.runner)
	source := func(context.Context) (recalcexec.Selection, error) {
		return recalcexec.Selection{Set: fx.set.Set, Members: fx.set.Members, Subjects: subjects, Columns: []string{"close", "volume"}}, nil
	}
	require.NoError(t, executor.Run(context.Background(), recalcexec.Window{Start: at(5), End: at(10)}, source,
		func(context.Context, time.Time, pipeline.Outcome) (bool, error) { return false, nil }))
	for _, m := range []int{7, 8, 9} {
		for _, s := range []string{"BTC", "ETH"} {
			r, ok := fx.rawResult(s, plainTag, m)
			require.True(t, ok, "%s 第 %d 分钟", s, m)
			require.NotNil(t, r.rank, "%s 第 %d 分钟的排名不能被写成 NULL", s, m)
		}
	}
}
