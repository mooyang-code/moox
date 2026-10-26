package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

func TestEstimateChunkMemoryGrowsWithPeriodsAndIgnoresUncompilable(t *testing.T) {
	factors := []domain.FactorDef{
		testPipelineFactor("short", domain.FactorTypeTimeSeries, 5, false),
		testPipelineFactor("long", domain.FactorTypeTimeSeries, 200, false),
	}
	one, err := EstimateChunkMemory(domain.AsMembers(factors...), 500, 1, 6)
	require.NoError(t, err)
	many, err := EstimateChunkMemory(domain.AsMembers(factors...), 500, 500, 6)
	require.NoError(t, err)
	require.Positive(t, one.Total())
	require.Greater(t, many.Total(), one.Total())
	require.Greater(t, many.Assemble, one.Assemble)
	// 回看窗口是常驻的列式窗口，不再计入读取；读取项只随目标周期数增长（目标区间的行）。
	require.Equal(t, int64(500*1*(2+6))*frameCellBytes, one.Read)
	require.Equal(t, int64(500*500*(2+6))*frameCellBytes, many.Read)
	// 回看 200 的节点要在 200 期窗口上计算：实时一期的计算内存也不小。
	require.Greater(t, one.Compute, int64(500*199*8))

	broken := domain.FactorDef{FactorID: "broken", Spec: "output: nope(close)\n"}
	withBroken, err := EstimateChunkMemory(domain.AsMembers(append(factors, broken)...), 500, 500, 6)
	require.NoError(t, err)
	require.Equal(t, many, withBroken)
	empty, err := EstimateChunkMemory(domain.AsMembers(broken), 500, 500, 6)
	require.NoError(t, err)
	require.Zero(t, empty.Total())
}

func TestMaxPeriodsWithinNarrowsAndRejects(t *testing.T) {
	factors := []domain.FactorDef{testPipelineFactor("mean", domain.FactorTypeTimeSeries, 20, false)}
	full, err := EstimateChunkMemory(domain.AsMembers(factors...), 500, 500, 6)
	require.NoError(t, err)
	got, err := MaxPeriodsWithin(domain.AsMembers(factors...), 500, 6, 500, full.Total())
	require.NoError(t, err)
	require.Equal(t, 500, got, "预算足够时不收窄")
	got, err = MaxPeriodsWithin(domain.AsMembers(factors...), 500, 6, 500, full.Total()/4)
	require.NoError(t, err)
	require.Less(t, got, 500)
	require.Positive(t, got)
	at, err := EstimateChunkMemory(domain.AsMembers(factors...), 500, got, 6)
	require.NoError(t, err)
	require.LessOrEqual(t, at.Total(), full.Total()/4)
	next, err := EstimateChunkMemory(domain.AsMembers(factors...), 500, got+1, 6)
	require.NoError(t, err)
	require.Greater(t, next.Total(), full.Total()/4, "取的是最大的可行周期数")
	_, err = MaxPeriodsWithin(domain.AsMembers(factors...), 500, 6, 500, 1024)
	require.ErrorIs(t, err, ErrMemoryBudget)
	got, err = MaxPeriodsWithin(domain.AsMembers(factors...), 500, 6, 500, 0)
	require.NoError(t, err)
	require.Equal(t, 500, got, "不限制")
}

// 实时周期估算超限：本期全部因子按计算失败处理，不尝试执行。
func TestComputeRejectsPeriodOverMemoryBudget(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("mean", domain.FactorTypeTimeSeries, 3, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	computation, err := newComputeRunner(Config{MaxMemory: 64}).Compute(context.Background(), plan, framesForBars(plan.Available, target, 3))
	require.NoError(t, err)
	require.ErrorIs(t, computation.Results[factor.FactorID]["BTC"].Err, ErrMemoryBudget)
	require.Equal(t, "degraded", computation.FactorStates[factor.FactorID].Status)

	computation, err = newComputeRunner(Config{MaxMemory: 1 << 30}).Compute(context.Background(), plan, framesForBars(plan.Available, target, 3))
	require.NoError(t, err)
	require.NoError(t, computation.Results[factor.FactorID]["BTC"].Err)
}

// 读取之前的预检：实时周期超限时不读取，本期全部因子按计算失败上报；补算超限以资源错误失败。
func TestRunPrechecksMemoryBeforeReading(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	subjects := []string{"BTC", "ETH"}
	clock := periodclock.Continuous{}
	store := newMemoryStorage(subjects, target, 10)
	runner := NewRunner(store, store.data(), clock, Config{MaxMemory: 64})
	plan, err := BuildLivePlan(clock, e2eLiveInput(target, subjects, []domain.FactorDef{e2eMean()}))
	require.NoError(t, err)
	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, 0, store.reads)
	require.Len(t, store.markers, 1)
	require.Equal(t, "degraded", store.markers[0].Factors[0].Status)

	recalc := plan
	recalc.Mode, recalc.TargetStart, recalc.TargetEnd = ModeRecalc, target.Add(-5*time.Minute), target.Add(time.Minute)
	_, err = runner.Run(context.Background(), recalc)
	require.ErrorIs(t, err, ErrMemoryBudget)
	require.Equal(t, 0, store.reads)
}

func TestCountSeriesCountsTags(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	loaded := framesForBars([]string{"BTC", "ETH"}, target, 2)
	loaded.Frames["BTC"].Rows = append(loaded.Frames["BTC"].Rows, []any{target, "venue:okx", 1.0})
	loaded = withView(t, loaded)
	require.Equal(t, 4, countSeries([]string{"BTC", "ETH", "SOL"}, loaded.View), "BTC 两个标签、ETH 一个、SOL 没有行按 1 条")
}
