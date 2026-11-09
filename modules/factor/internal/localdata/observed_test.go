package localdata

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

const rankMeanExpr = "ts_mean(cs_rank(close), 3)"

func observedRank(id string) domain.FactorDef {
	def := domain.MustDefinition(id, id, "cross_section_history: observed\noutput: "+rankMeanExpr+"\n")
	def.AllowPartialUniverse = true
	return def
}

func currentRank(id string) domain.FactorDef {
	def := domain.MustDefinition(id, id, "output: "+rankMeanExpr+"\n")
	def.AllowPartialUniverse = true
	return def
}

// observedSource 写入 A、B、C（close 基数 100、300、500，第 0～10 分钟）与 OLD（基数 400，只到第 9 分钟，之后下架）。
func observedSource(fx *fixture) {
	minutes := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	fx.st.put("A", 100, minutes...)
	fx.st.put("B", 300, minutes...)
	fx.st.put("C", 500, minutes...)
	fx.st.put("OLD", 400, minutes[:10]...)
}

func (fx *fixture) value(t *testing.T, subject string, m int, column string) float64 {
	t.Helper()
	value, ok := fx.result(subject, m).Fields[column].(float64)
	require.True(t, ok, "%s 第 %d 分钟没有 %s 的值：%v", subject, m, column, fx.result(subject, m).Fields)
	return value
}

// 标的离开全集（下架、移出）后仍有本地数据：observed 的历史截面仍包含它，与它离开前的截面一致；current 只用本期可用的标的。
// 同一任务混合两种口径时，current 成员与只有 current 成员的任务逐位相同；离开的标的不产生结果行。
func TestObservedHistoryKeepsDepartedSubject(t *testing.T) {
	fx := newFixture(t, observedRank("obs"), currentRank("cur"))
	observedSource(fx)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B", "C", "OLD"}))
	fx.live(t, at(9), []string{"A", "B", "C", "OLD"})
	// 第 10 分钟 OLD 已移出全集：窗口仍保留它（observed 任务），但不再同步、不再输出。
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B", "C"}))
	outcome := fx.live(t, at(10), []string{"A", "B", "C"})
	require.Equal(t, "complete", outcome.Status)

	// 第 8、9 分钟的截面有 OLD：A、B、C、OLD 的名次为 1/4、2/4、4/4、3/4；第 10 分钟只有 A、B、C。
	require.InDelta(t, (0.25+0.25+1.0/3)/3, fx.value(t, "A", 10, "obs"), 1e-12)
	require.InDelta(t, (0.5+0.5+2.0/3)/3, fx.value(t, "B", 10, "obs"), 1e-12)
	require.InDelta(t, 1.0, fx.value(t, "C", 10, "obs"), 1e-12)
	require.Empty(t, fx.result("OLD", 10).Fields, "离开全集的标的不输出")
	require.Equal(t, 1.0/3, fx.value(t, "A", 10, "cur"))

	// 只有 current 成员的任务：面板不扩大、不传范围掩码，结果与混合任务里的 current 成员逐位相同。
	alone := newFixture(t, currentRank("cur"))
	observedSource(alone)
	require.NoError(t, alone.data.Prepare(context.Background(), alone.set, []string{"A", "B", "C", "OLD"}))
	alone.live(t, at(9), []string{"A", "B", "C", "OLD"})
	require.NoError(t, alone.data.Prepare(context.Background(), alone.set, []string{"A", "B", "C"}))
	alone.live(t, at(10), []string{"A", "B", "C"})
	for _, subject := range []string{"A", "B", "C"} {
		require.Equal(t, math.Float64bits(alone.value(t, subject, 10, "cur")), math.Float64bits(fx.value(t, subject, 10, "cur")), subject)
	}
	// 只有 current 成员时窗口按任务标的保留，OLD 被释放。
	require.False(t, alone.data.st(sourceOf(alone.set.Set)).window.Has("OLD"))
	require.True(t, fx.data.st(sourceOf(fx.set.Set)).window.Has("OLD"))
}

// 重新载入（模拟重启）：新的数据层只按本地存储的内容组装面板，结果与重启前逐位相同；补算读磁盘的路径也相同。
func TestObservedHistorySurvivesReload(t *testing.T) {
	fx := newFixture(t, observedRank("obs"))
	observedSource(fx)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B", "C", "OLD"}))
	fx.live(t, at(9), []string{"A", "B", "C", "OLD"})
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B", "C"}))
	fx.live(t, at(10), []string{"A", "B", "C"})
	before := map[string]float64{}
	for _, subject := range []string{"A", "B", "C"} {
		before[subject] = fx.value(t, subject, 10, "obs")
	}

	// 重启：同一个本地存储、新的数据层；全集里已经没有 OLD。
	restarted := New(Config{Store: fx.data.Store(), Storage: fx.st, Admit: memadmit.New(0), Workers: 2, BatchSubjects: 2,
		Now: func() time.Time { return fx.now }})
	fx.data = restarted
	fx.runner = pipeline.NewRunner(fx.st, restarted, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	require.NoError(t, restarted.Prepare(context.Background(), fx.set, []string{"A", "B", "C"}))
	fx.st.mu.Lock()
	fx.st.results = map[string]storageio.ResultRow{}
	fx.st.mu.Unlock()
	fx.live(t, at(10), []string{"A", "B", "C"})
	for subject, want := range before {
		require.Equal(t, math.Float64bits(want), math.Float64bits(fx.value(t, subject, 10, "obs")), subject)
	}

	// 补算读磁盘：同一期的结果相同。
	fx.st.mu.Lock()
	fx.st.results = map[string]storageio.ResultRow{}
	fx.st.mu.Unlock()
	subjects := []string{"A", "B", "C"}
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(10), TargetEnd: at(11),
		Expected: subjects, Available: subjects, CarryColumns: []string{"close", "volume"}}
	_, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	for subject, want := range before {
		require.Equal(t, math.Float64bits(want), math.Float64bits(fx.value(t, subject, 10, "obs")), subject)
	}
}

// 新加入全集的标的：Prepare 从 Storage 回补它的历史，它在加入之前的历史截面里也参与排名。
func TestObservedHistoryIncludesBackfilledNewcomer(t *testing.T) {
	fx := newFixture(t, observedRank("obs"))
	observedSource(fx)
	fx.st.put("NEW", 200, 5, 6, 7, 8, 9, 10)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B", "C", "OLD"}))
	fx.live(t, at(9), []string{"A", "B", "C", "OLD"})
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"A", "B", "C", "NEW"}))
	fx.live(t, at(10), []string{"A", "B", "C", "NEW"})
	// 第 8、9 分钟：A、NEW、B、OLD、C 五个标的，A 的名次 1/5；第 10 分钟 A、NEW、B、C，A 为 1/4。
	require.InDelta(t, (0.2+0.2+0.25)/3, fx.value(t, "A", 10, "obs"), 1e-12)
	require.InDelta(t, (0.4+0.4+0.5)/3, fx.value(t, "NEW", 10, "obs"), 1e-12)
}
