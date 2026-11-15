package engine

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// signal 改写对照（批 3 T0 第 1 项）：signal 规则按标准写法改写成 rank 加 hold（显式 pool、score 取常数、select.top 取足够大、
// entry 与 exit 挪进 hold，不带 allow_empty），同一组输入逐期比较结果状态、跳过原因与目标（标的、市场、权重、hold 标记）。
// 明细原因两边文字不同，不比。已知差别（执行计划 3.1 的差别表）每行一个用例，断言两边各自的值。

// signalRewriteDSL 是 testdata/baseline/signal.json 那份 DSL 的标准改写。
const signalRewriteDSL = `name: baseline_signal
rules:
  - id: long
    type: rank
    pool: [A, B]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "bars[-1].close <= bars[-1].ma && close > ma"
      exit: "close < ma"
    weight: {total: 0.4}
  - id: short
    type: rank
    pool: [C]
    side: short
    score: "0"
    select: {top: 10000}
    hold:
      entry: "close < ma"
      exit: "close > ma"
    weight: {total: 0.3}
    stop: {loss: 0.05, cooldown_bars: 1}
portfolio:
  max_missing: 0.5
`

// signalRewriteRun 逐期求值，每期的前序状态是上一期决策的状态（与 baselineEngineRun 同一口径）。
func signalRewriteRun(t *testing.T, raw string, columns []string, bars []baselineEngineBar) []Decision {
	t.Helper()
	decisions, _ := baselineEngineRun(t, baselineEngineScenario{name: "signal_rewrite", dsl: raw, columns: columns, bars: bars})
	return decisions
}

// signalRewriteTargets 把一期的目标整理成可比较的列表。
func signalRewriteTargets(decision Decision) []baselineEngineTarget {
	targets := make([]baselineEngineTarget, 0, len(decision.Targets))
	for _, target := range decision.Targets {
		targets = append(targets, baselineEngineTarget{InstrumentID: target.InstrumentID, Market: target.Market, Weight: target.Weight.String(), Hold: target.Hold})
	}
	return targets
}

// signalRewriteEqual 逐期比较两边的结果状态、跳过原因与目标。
func signalRewriteEqual(t *testing.T, signal, rank []Decision) {
	t.Helper()
	require.Equal(t, len(signal), len(rank))
	for i := range signal {
		require.Equal(t, signal[i].Status, rank[i].Status, "第 %d 期的结果状态", i)
		require.Equal(t, signal[i].SkipReason, rank[i].SkipReason, "第 %d 期的跳过原因", i)
		require.Equal(t, signalRewriteTargets(signal[i]), signalRewriteTargets(rank[i]), "第 %d 期的目标", i)
	}
}

// 金样 signal.json 的 DSL 与它的标准改写逐期一致；signal 一侧同时与金样文件对拍，保证比较的基准就是金样。
func TestSignalRewriteMatchesBaseline(t *testing.T) {
	var scenario baselineEngineScenario
	for _, item := range baselineEngineScenarios() {
		if item.name == "signal" {
			scenario = item
		}
	}
	require.Equal(t, "signal", scenario.name, "基线场景里没有 signal")
	signal, golden := baselineEngineRun(t, scenario)
	if !baselineUpdate() {
		baselineCompare(t, "testdata/baseline/signal.json", golden)
	}
	rank := signalRewriteRun(t, signalRewriteDSL, scenario.columns, scenario.bars)
	signalRewriteEqual(t, signal, rank)

	// 这份输入要覆盖到的情形：第一期缺数跳过、入场、出场、止损、缺数跳过后恢复。
	require.Equal(t, StatusSkipped, rank[0].Status)
	require.Equal(t, SkipTooManyMissing, rank[0].SkipReason, "第 0 期没有上一根，两边都应因缺数跳过")
	require.Equal(t, SkipTooManyMissing, rank[5].SkipReason, "第 5 期 C 的 ma 缺失，两边都应因缺数跳过")
	require.Equal(t, 1, rank[4].Summary.Rules["short"].StopLoss, "第 4 期空头 C 应止损")
	require.NotEmpty(t, rank[1].Targets)
	require.Empty(t, rank[6].Targets)
}

// signalRewriteRows 把「标的 → sig 值」写成永续市场的一期输入；值为 NaN 以外的任意数，缺的标的不写。
func signalRewriteRows(column string, values map[string]float64) baselineEngineBar {
	rows := map[string]map[string]float64{}
	for id, value := range values {
		rows[id] = map[string]float64{column: value}
	}
	return baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, rows)}
}

// 差别一：池里的标的全部年龄不够。signal 池为空时不做整期跳过的检查，这条规则不出目标、其他规则照常；
// rank 加 hold（不带 allow_empty）预期集合减去年龄剔除后为 0，整期跳过。
func TestSignalRewriteDiffersWhenPoolAgedOut(t *testing.T) {
	const other = `
  - id: other
    type: rank
    score: "sig"
    select: {top: 1}
    weight: {total: 0.5}
portfolio:
  max_missing: 1
`
	signalDSL := `name: aged
rules:
  - id: timing
    type: signal
    pool: [A]
    entry: "sig > 0"
    exit: "sig < 0"
    weight: {total: 0.2}` + other
	rankDSL := `name: aged
rules:
  - id: timing
    type: rank
    pool: [A]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "sig < 0"
    weight: {total: 0.2}` + other
	bar := signalRewriteRows("sig", map[string]float64{"A": 1, "B": 2, "C": 3})
	// 年龄剔除按规则给出（Frame.AgedOut）：只有固定池规则 timing 的 A 年龄不够。
	run := func(raw string) Decision {
		program := compile(t, raw, "sig")
		frame := baselineEngineFrame(program, 100, bar, nil)
		frame.AgedOut["timing"] = []string{"A"}
		return evaluate(t, program, frame, State{})
	}

	signal := run(signalDSL)
	require.Equal(t, StatusOK, signal.Status)
	require.Equal(t, 1, signal.Summary.Rules["timing"].AgedOut)
	require.Equal(t, map[string]string{TargetKey("C", dsl.MarketSwap): "0.5"}, baselineTargetKeys(signal), "signal：这条规则不出目标，其他规则照常")

	rank := run(rankDSL)
	require.Equal(t, 1, rank.Summary.Rules["timing"].AgedOut)
	require.Equal(t, StatusSkipped, rank.Status)
	require.Equal(t, SkipUniverseTooSmall, rank.SkipReason, "rank 加 hold：池里的标的全部年龄不够时整期跳过")
	require.Empty(t, rank.Targets)
}

// 差别二：min_universe 大于池里的标的数。signal 不受 min_universe 约束；rank 加 hold 不写 allow_empty 时保存被拒，
// 写了之后这条规则完全不参与整期跳过的检查：持仓缺数时 signal 整期跳过、持仓不动，rank 加 hold 按 exit_missing 出场。
func TestSignalRewriteDiffersWhenMinUniverseExceedsPool(t *testing.T) {
	const portfolio = `
portfolio:
  min_universe: 2
  max_missing: 0.5
`
	signalDSL := `name: small
rules:
  - id: timing
    type: signal
    pool: [A]
    entry: "sig > 0"
    exit: "sig < 0"
    weight: {total: 0.2}` + portfolio
	rankRule := `name: small
rules:
  - id: timing
    type: rank
    pool: [A]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "sig < 0"
    weight: {total: 0.2}`
	// 第 1 期 A 有行但 sig 为空（没有失败证据）：按缺数处理，不冻结。
	missing := baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, map[string]map[string]float64{"A": {}})}
	bars := []baselineEngineBar{signalRewriteRows("sig", map[string]float64{"A": 1}), missing}
	held := map[string]string{TargetKey("A", dsl.MarketSwap): "0.2"}

	signal := signalRewriteRun(t, signalDSL, []string{"sig"}, bars)
	require.Equal(t, StatusOK, signal[0].Status, "signal 不受 min_universe 约束")
	require.Equal(t, held, baselineTargetKeys(signal[0]))
	require.Equal(t, SkipTooManyMissing, signal[1].SkipReason, "signal：持仓缺数超过 max_missing 时整期跳过")
	require.Equal(t, []string{"A"}, signal[1].State.Rules["timing"].Held, "signal：整期跳过时持仓不动")

	// 标准写法（不带 allow_empty）在保存时被拒。
	strategy, err := dsl.Parse([]byte(rankRule + portfolio))
	if err == nil {
		_, err = compileMode(strategy, []string{"sig"})
	}
	require.ErrorContains(t, err, "min_universe")

	rank := signalRewriteRun(t, rankRule+"\n    allow_empty: true"+portfolio, []string{"sig"}, bars)
	require.Equal(t, StatusOK, rank[0].Status)
	require.Equal(t, held, baselineTargetKeys(rank[0]))
	require.Equal(t, StatusOK, rank[1].Status, "rank 加 hold 写了 allow_empty 后不参与整期跳过的检查")
	require.Empty(t, rank[1].Targets)
	require.Empty(t, rank[1].State.Rules["timing"].Held, "rank 加 hold：缺数的持仓出场")
	item := baselineItem(rank[1], "timing", "A")
	require.Equal(t, StageMissing, item.Stage)
	require.Contains(t, item.Reason, reasonExitMissing)
}

// 差别三：有冻结标的时的权重。signal 把预算只在没冻结的持有标的之间平分，冻结的份额另由组合层保持，合计可以超过规则预算；
// rank 加 hold 把预算在全部持有标的（含冻结）之间平分，合计不超过规则预算。
func TestSignalRewriteDiffersInFrozenWeights(t *testing.T) {
	const portfolio = `
portfolio:
  max_missing: 1
`
	signalDSL := `name: frozen
rules:
  - id: timing
    type: signal
    pool: [A, B, C]
    entry: "sig > 0"
    exit: "sig < 0"
    weight: {total: 0.6}` + portfolio
	rankDSL := `name: frozen
rules:
  - id: timing
    type: rank
    pool: [A, B, C]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "sig < 0"
    weight: {total: 0.6}` + portfolio
	// 第 1 期 A 所在市场不可用（没有行，带 market_unavailable 证据）：已持有的 A 冻结。
	down := signalRewriteRows("sig", map[string]float64{"B": 1, "C": 1})
	down.Failed = map[string]map[string]map[string]string{dsl.MarketSwap: {"sig": {"A": FailedMarket}}}
	bars := []baselineEngineBar{signalRewriteRows("sig", map[string]float64{"A": 1, "B": 1, "C": 1}), down}
	key := func(id string) string { return TargetKey(id, dsl.MarketSwap) }
	entered := map[string]string{key("A"): "0.2", key("B"): "0.2", key("C"): "0.2"}

	signal := signalRewriteRun(t, signalDSL, []string{"sig"}, bars)
	require.Equal(t, entered, baselineTargetKeys(signal[0]))
	require.Equal(t, map[string]string{key("A"): "0.2(hold)", key("B"): "0.3", key("C"): "0.3"}, baselineTargetKeys(signal[1]),
		"signal：预算 0.6 只在没冻结的 B、C 之间平分，冻结的 A 另外保持 0.2")

	rank := signalRewriteRun(t, rankDSL, []string{"sig"}, bars)
	require.Equal(t, entered, baselineTargetKeys(rank[0]))
	require.Equal(t, map[string]string{key("A"): "0.2(hold)", key("B"): "0.2", key("C"): "0.2"}, baselineTargetKeys(rank[1]),
		"rank 加 hold：预算 0.6 在 A、B、C 之间平分，冻结的 A 由组合层保持")
}

// 差别四：明细原因只是文字不同，目标一致。出场、不满足入场、入场与出场同根成立三种情形各断言两边的原因。
func TestSignalRewriteDiffersInItemReasons(t *testing.T) {
	const portfolio = `
portfolio:
  max_missing: 1
`
	signalDSL := `name: reasons
rules:
  - id: timing
    type: signal
    pool: [A, B, C]
    entry: "sig > 0"
    exit: "out > 0"
    weight: {total: 0.6}` + portfolio
	rankDSL := `name: reasons
rules:
  - id: timing
    type: rank
    pool: [A, B, C]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "out > 0"
    weight: {total: 0.6}` + portfolio
	row := func(sig, out float64) map[string]float64 { return map[string]float64{"sig": sig, "out": out} }
	bars := []baselineEngineBar{
		// 第 0 期：A 入场；B 不满足入场；C 入场与出场同根成立，不入场。
		{Rows: baselineOne(dsl.MarketSwap, map[string]map[string]float64{"A": row(1, 0), "B": row(0, 0), "C": row(1, 1)})},
		// 第 1 期：A 出场。
		{Rows: baselineOne(dsl.MarketSwap, map[string]map[string]float64{"A": row(1, 1), "B": row(0, 0), "C": row(0, 0)})},
	}
	columns := []string{"sig", "out"}
	signal := signalRewriteRun(t, signalDSL, columns, bars)
	rank := signalRewriteRun(t, rankDSL, columns, bars)
	signalRewriteEqual(t, signal, rank)
	require.Equal(t, map[string]string{TargetKey("A", dsl.MarketSwap): "0.6"}, baselineTargetKeys(rank[0]))
	require.Empty(t, rank[1].Targets)

	require.Equal(t, "no_entry", baselineItem(signal[0], "timing", "B").Reason)
	require.Equal(t, "entry_and_exit", baselineItem(signal[0], "timing", "C").Reason)
	require.Equal(t, "exit", baselineItem(signal[1], "timing", "A").Reason)

	require.Equal(t, reasonHoldEntered, baselineItem(rank[0], "timing", "A").Reason)
	require.Equal(t, reasonHoldNotEntered, baselineItem(rank[0], "timing", "B").Reason)
	require.Equal(t, reasonHoldNotEntered, baselineItem(rank[0], "timing", "C").Reason)
	require.Equal(t, reasonExitCondition, baselineItem(rank[1], "timing", "A").Reason)
}
