package pipeline

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// testPipelineFactor 返回回看为 lookback 的定义：时序因子是 ts_mean(close, lookback)，截面因子是对它做 cs_rank。
func testPipelineFactor(id, factorType string, lookback int, partial bool) domain.FactorDef {
	expression := fmt.Sprintf("ts_mean(close, %d)", lookback)
	if factorType == domain.FactorTypeCrossSection {
		expression = "cs_rank(" + expression + ")"
	}
	def := domain.MustDefinition(id, id, "output: "+expression+"\n")
	def.AllowPartialUniverse = partial
	return def
}

func computePlan(target time.Time, factors []domain.FactorDef, subjects []string) Plan {
	return Plan{
		Mode: ModeLive, Set: domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m"},
		Members: domain.AsMembers(factors...), TargetStart: target.Add(-time.Minute), TargetEnd: target.Add(time.Minute),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"}, WriteCarry: true,
	}
}

// framesForBars 为每个标的造到 target 为止的 bars 根 K 线，close = 标的序号 * 100 + 分钟序号。
func framesForBars(subjects []string, target time.Time, bars int) LoadResult {
	frames := make(map[string]*storageio.Frame, len(subjects))
	for s, subject := range subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		for i := bars - 1; i >= 0; i-- {
			at := target.Add(-time.Duration(i) * time.Minute)
			frame.Rows = append(frame.Rows, []any{at, "venue:binance", float64((s+1)*100 + bars - 1 - i)})
		}
		frames[subject] = frame
	}
	return mustView(LoadResult{Frames: frames, Available: append([]string(nil), subjects...)})
}

// framesFor 为每个标的造目标时刻的一根 K 线，close = 10。
func framesFor(subjects []string, target time.Time) LoadResult {
	frames := make(map[string]*storageio.Frame, len(subjects))
	for _, subject := range subjects {
		frames[subject] = &storageio.Frame{
			SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"},
			Rows: [][]any{{target, "venue:binance", 10.0}},
		}
	}
	return mustView(LoadResult{Frames: frames, Available: append([]string(nil), subjects...)})
}

func newComputeRunner(cfg Config) *Runner {
	return NewRunner(&fakeStore{}, nil, periodclock.Continuous{}, cfg)
}

// valueAt 取某个结果在某个时刻的第一个输出值。
func valueAt(t *testing.T, item ItemResult, at time.Time) any {
	t.Helper()
	require.NoError(t, item.Err)
	for _, row := range item.Rows {
		if row[0].(time.Time).Equal(at) {
			return row[len(row)-1]
		}
	}
	t.Fatalf("没有 %s 的结果行：%v", at, item.Rows)
	return nil
}

func TestComputeTimeSeriesComputesTargetRowsOnly(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	mean := testPipelineFactor("mean", domain.FactorTypeTimeSeries, 3, false)
	double := domain.MustDefinition("double", "double", "output: close * 2\n")
	plan := computePlan(target, []domain.FactorDef{mean, double}, []string{"BTC", "ETH"})
	loaded := framesForBars(plan.Available, target, 3)
	loaded.Frames["ETH"].Rows = loaded.Frames["ETH"].Rows[1:] // ETH 只有 2 根：窗口不完整
	loaded = withView(t, loaded)

	computation, err := newComputeRunner(Config{ComputeWorkers: 2}).Compute(context.Background(), plan, loaded)

	require.NoError(t, err)
	for _, factor := range []domain.FactorDef{mean, double} {
		require.Equal(t, "complete", computation.FactorStates[factor.FactorID].Status)
		require.Equal(t, factor.SpecHash, computation.FactorStates[factor.FactorID].SourceHash)
		require.Equal(t, domain.DefinitionHash(factor), computation.FactorStates[factor.FactorID].DefinitionHash)
		require.Len(t, computation.Results[factor.FactorID], 2)
	}
	btc := computation.Results["mean"]["BTC"]
	require.Equal(t, []string{"data_time", "series_tag", "mean"}, btc.Columns)
	require.Len(t, btc.Rows, 1, "实时周期只输出目标时刻")
	require.Equal(t, []any{target, "venue:binance", 101.0}, btc.Rows[0])
	require.Nil(t, valueAt(t, computation.Results["mean"]["ETH"], target), "严格窗口：不完整时为 null")
	require.Equal(t, 404.0, valueAt(t, computation.Results["double"]["ETH"], target))
}

func TestComputeCrossSectionSkipsWhenPartialUniverseNotAllowed(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	loaded := framesForBars([]string{"BTC"}, target, 2)
	loaded.FailedSubjects = []string{"ETH"}
	computation, err := newComputeRunner(Config{}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, "skipped", computation.FactorStates[factor.FactorID].Status)
	require.Empty(t, computation.Results[factor.FactorID])
}

func TestComputeCrossSectionRunsOnAvailableWhenAllowed(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, true)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC", "ETH", "SOL"})
	loaded := framesForBars([]string{"BTC", "SOL"}, target, 2)
	loaded.FailedSubjects = []string{"ETH"}
	computation, err := newComputeRunner(Config{}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, "degraded", computation.FactorStates[factor.FactorID].Status)
	require.Equal(t, []string{"ETH"}, computation.FactorStates[factor.FactorID].FailedSubjects)
	item := computation.Results[factor.FactorID][""]
	require.Equal(t, []string{"data_time", "series_tag", "subject_id", "spread"}, item.Columns)
	require.Len(t, item.Rows, 2)
	ranks := map[string]any{}
	for _, row := range item.Rows {
		ranks[row[2].(string)] = row[3]
	}
	require.Equal(t, map[string]any{"BTC": 0.5, "SOL": 1.0}, ranks, "只在可用的标的里排名")
	require.Equal(t, periodOK, computation.CrossPeriods[factor.FactorID][target.Unix()])
}

func TestComputeRecalcOutputsEveryTargetPeriod(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := recalcPlan(start, []domain.FactorDef{factor}, []string{"BTC", "ETH"})
	loaded := recalcFrames(plan.Available, start, 3)
	computation, err := newComputeRunner(Config{ComputeWorkers: 2}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	btc := computation.Results[factor.FactorID]["BTC"]
	require.Len(t, btc.Rows, 3)
	// close 依次为 1, 2, 3, 4（index -1..2）；目标期的两期均值是 1.5, 2.5, 3.5。
	for i, want := range []float64{1.5, 2.5, 3.5} {
		require.Equal(t, want, valueAt(t, btc, start.Add(time.Duration(i)*time.Minute)))
	}
}

// 引擎侧编译失败只影响该因子：对全部标的失败并计数，其他因子照常（设计 7.6）。
func TestComputeEngineCompileFailureFailsOnlyThatFactor(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	good := testPipelineFactor("good", domain.FactorTypeTimeSeries, 1, false)
	broken := domain.FactorDef{FactorID: "broken", Name: "broken", Spec: "output: from_the_future(close)\n",
		Outputs: []string{"broken"}, LookbackPeriods: 1, FactorType: domain.FactorTypeTimeSeries}
	var counted atomic.Int32
	plan := computePlan(target, []domain.FactorDef{good, broken}, []string{"BTC", "ETH"})
	runner := newComputeRunner(Config{OnCompileError: func(taskID, factorID string) {
		require.Equal(t, "broken", factorID)
		counted.Add(1)
	}})

	computation, err := runner.Compute(context.Background(), plan, framesFor(plan.Available, target))

	require.NoError(t, err)
	require.EqualValues(t, 1, counted.Load())
	require.Equal(t, "degraded", computation.FactorStates["broken"].Status)
	require.Equal(t, []string{"BTC", "ETH"}, computation.FactorStates["broken"].FailedSubjects)
	require.ErrorContains(t, computation.Results["broken"]["BTC"].Err, "编译失败")
	require.Equal(t, "complete", computation.FactorStates["good"].Status)
	require.Equal(t, 10.0, valueAt(t, computation.Results["good"]["BTC"], target))
}

// 因子输入列不是数值字段（窗口里没有）：整个程序失败，本期全部因子按计算失败处理。
func TestComputeRejectsInputColumnMissingFromWindow(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := domain.MustDefinition("lastnote", "lastnote", "output: note * 2\n")
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"BTC"})
	computation, err := newComputeRunner(Config{}).Compute(context.Background(), plan, framesFor(plan.Available, target))
	require.NoError(t, err)
	require.ErrorContains(t, computation.Results[factor.FactorID]["BTC"].Err, "窗口缺少输入列")
	require.Equal(t, "degraded", computation.FactorStates[factor.FactorID].Status)
}

func TestComputeBudgetExceededFailsThePeriod(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factor := testPipelineFactor("momentum", domain.FactorTypeTimeSeries, 2, false)
	plan := computePlan(target, []domain.FactorDef{factor}, []string{"A", "B"})
	plan.Budget = time.Nanosecond
	time.Sleep(time.Millisecond)
	computation, err := newComputeRunner(Config{}).Compute(context.Background(), plan, framesForBars(plan.Available, target, 2))
	require.NoError(t, err)
	require.True(t, computation.BudgetHit)
	require.ErrorIs(t, computation.Results[factor.FactorID]["A"].Err, context.DeadlineExceeded)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = newComputeRunner(Config{}).Compute(cancelled, computePlan(target, []domain.FactorDef{factor}, []string{"A"}), framesFor([]string{"A"}, target))
	require.ErrorIs(t, err, context.Canceled)
}

// 结果与并行度无关，逐位相同（实时写入的提交 ID 依赖它）；一个标的的多个 series_tag 各是一条序列。
func TestComputeIsDeterministicAcrossWorkersAndTags(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	factors := []domain.FactorDef{
		testPipelineFactor("mean", domain.FactorTypeTimeSeries, 3, false),
		testPipelineFactor("rank", domain.FactorTypeCrossSection, 2, true),
	}
	subjects := make([]string, 40)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("S%02d", i)
	}
	plan := recalcPlan(start, factors, subjects)
	plan.TargetEnd = start.Add(20 * time.Minute)
	loaded := recalcFrames(subjects, start, 20)
	for s, subject := range subjects {
		frame := loaded.Frames[subject]
		rows := len(frame.Rows)
		for i := 0; i < rows; i++ {
			row := frame.Rows[i]
			frame.Rows = append(frame.Rows, []any{row[0], "venue:okx", float64(s*7%13) + float64(i)/3})
		}
	}
	loaded = withView(t, loaded)
	single, err := newComputeRunner(Config{ComputeWorkers: 1}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	parallel, err := newComputeRunner(Config{ComputeWorkers: 8}).Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	require.Equal(t, single.Results, parallel.Results)
	require.Len(t, single.Results["mean"]["S01"].Rows, 40, "两个标签 × 20 期")
}

func recalcPlan(start time.Time, factors []domain.FactorDef, subjects []string) Plan {
	return Plan{
		Mode: ModeRecalc, Set: domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "dataset_bars", ResultDatasetID: "dataset_factor_bars", Freq: "1m"},
		Members: domain.AsMembers(factors...), TargetStart: start, TargetEnd: start.Add(3 * time.Minute),
		Expected: append([]string(nil), subjects...), Available: append([]string(nil), subjects...), CarryColumns: []string{"close"}, WriteCarry: true,
	}
}

func recalcFrames(subjects []string, start time.Time, targetPeriods int) LoadResult {
	frames := make(map[string]*storageio.Frame, len(subjects))
	for _, subject := range subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		for index := -1; index < targetPeriods; index++ {
			frame.Rows = append(frame.Rows, []any{start.Add(time.Duration(index) * time.Minute), "venue:binance", float64(index + 2)})
		}
		frames[subject] = frame
	}
	return mustView(LoadResult{Frames: frames, Available: append([]string(nil), subjects...)})
}

// mustView 按 Frames 生成列式视图；帧里有非数值值时 panic（用于构造合法的测试输入）。
func mustView(loaded LoadResult) LoadResult {
	view, err := viewFromFrames(loaded.Frames, 60)
	if err != nil {
		panic(err)
	}
	loaded.View = view
	return loaded
}
