package engine

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// 固定标的择时的写法：显式 pool、score 取常数、select.top 取足够大、进出场条件写在 hold 里，不带 allow_empty。
// testdata/baseline/signal.json 是删除 signal 规则类型之前用 signal 写法生成的金样，只读、不再生成；
// 这里用同一组输入跑 rank 写法，逐期的结果状态、跳过原因与目标（标的、市场、权重、hold 标记）必须与它一致。
// 明细原因与规则状态两种写法本来就不同，不比；rank 写法自己的完整输出在 testdata/baseline/hold_pool.json。

// holdPoolRun 逐期求值，每期的前序状态是上一期决策的状态（与 baselineEngineRun 同一口径）。
func holdPoolRun(t *testing.T, raw string, columns []string, bars []baselineEngineBar) []Decision {
	t.Helper()
	decisions, _ := baselineEngineRun(t, baselineEngineScenario{name: "hold_pool", dsl: raw, columns: columns, bars: bars})
	return decisions
}

func TestHoldPoolMatchesSignalBaseline(t *testing.T) {
	var scenario baselineEngineScenario
	for _, item := range baselineEngineScenarios() {
		if item.name == "hold_pool" {
			scenario = item
		}
	}
	require.Equal(t, "hold_pool", scenario.name, "基线场景里没有 hold_pool")
	raw, err := os.ReadFile("testdata/baseline/signal.json")
	require.NoError(t, err)
	var golden struct {
		Bars []struct {
			Input      baselineEngineBar      `json:"input"`
			Status     string                 `json:"status"`
			SkipReason string                 `json:"skip_reason"`
			Targets    []baselineEngineTarget `json:"targets"`
		} `json:"bars"`
	}
	require.NoError(t, json.Unmarshal(raw, &golden))
	require.Len(t, golden.Bars, len(scenario.bars))
	rank := holdPoolRun(t, scenario.dsl, scenario.columns, scenario.bars)
	for i, want := range golden.Bars {
		require.Equal(t, want.Input, scenario.bars[i], "第 %d 期的输入应与金样相同", i)
		require.Equal(t, want.Status, rank[i].Status, "第 %d 期的结果状态", i)
		require.Equal(t, want.SkipReason, rank[i].SkipReason, "第 %d 期的跳过原因", i)
		targets := make([]baselineEngineTarget, 0, len(rank[i].Targets))
		for _, target := range rank[i].Targets {
			targets = append(targets, baselineEngineTarget{InstrumentID: target.InstrumentID, Market: target.Market, Weight: target.Weight.String(), Hold: target.Hold})
		}
		require.Equal(t, want.Targets, targets, "第 %d 期的目标", i)
	}

	// 这份输入要覆盖到的情形：第一期缺数跳过、入场、出场、止损、缺数跳过后恢复。
	require.Equal(t, StatusSkipped, rank[0].Status)
	require.Equal(t, SkipTooManyMissing, rank[0].SkipReason, "第 0 期没有上一根，应因缺数跳过")
	require.Equal(t, SkipTooManyMissing, rank[5].SkipReason, "第 5 期 C 的 ma 缺失，应因缺数跳过")
	require.Equal(t, 1, rank[4].Summary.Rules["short"].StopLoss, "第 4 期空头 C 应止损")
	require.NotEmpty(t, rank[1].Targets)
	require.Empty(t, rank[6].Targets)
}

// holdPoolRows 把「标的 → sig 值」写成永续市场的一期输入；缺的标的不写。
func holdPoolRows(column string, values map[string]float64) baselineEngineBar {
	rows := map[string]map[string]float64{}
	for id, value := range values {
		rows[id] = map[string]float64{column: value}
	}
	return baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, rows)}
}

// 池里的标的全部年龄不够：预期集合减去年龄剔除后为 0，整期跳过（其他规则也不出目标）。
func TestHoldPoolSkipsWhenPoolAgedOut(t *testing.T) {
	program := compile(t, `name: aged
rules:
  - id: timing
    type: rank
    pool: [A]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "sig < 0"
    weight: {total: 0.2}
  - id: other
    type: rank
    score: "sig"
    select: {top: 1}
    weight: {total: 0.5}
portfolio:
  max_missing: 1
`, "sig")
	frame := baselineEngineFrame(program, 100, holdPoolRows("sig", map[string]float64{"A": 1, "B": 2, "C": 3}), nil)
	// 年龄剔除按规则给出（Frame.AgedOut）：只有固定池规则 timing 的 A 年龄不够。
	frame.AgedOut["timing"] = []string{"A"}
	decision := evaluate(t, program, frame, State{})
	require.Equal(t, 1, decision.Summary.Rules["timing"].AgedOut)
	require.Equal(t, StatusSkipped, decision.Status)
	require.Equal(t, SkipUniverseTooSmall, decision.SkipReason)
	require.Empty(t, decision.Targets)
}

// min_universe 大于池里的标的数：不写 allow_empty 时保存被拒；写了之后这条规则不参与整期跳过的检查，持仓缺数时按 exit_missing 出场。
func TestHoldPoolSmallerThanMinUniverseNeedsAllowEmpty(t *testing.T) {
	const portfolio = `
portfolio:
  min_universe: 2
  max_missing: 0.5
`
	rule := `name: small
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
	strategy, err := dsl.Parse([]byte(rule + portfolio))
	if err == nil {
		_, err = compileMode(strategy, []string{"sig"})
	}
	require.ErrorContains(t, err, "min_universe")

	// 第 1 期 A 有行但 sig 为空（没有失败证据）：按缺数处理，不冻结。
	missing := baselineEngineBar{Rows: baselineOne(dsl.MarketSwap, map[string]map[string]float64{"A": {}})}
	bars := []baselineEngineBar{holdPoolRows("sig", map[string]float64{"A": 1}), missing}
	decisions := holdPoolRun(t, rule+"\n    allow_empty: true"+portfolio, []string{"sig"}, bars)
	require.Equal(t, StatusOK, decisions[0].Status)
	require.Equal(t, map[string]string{TargetKey("A", dsl.MarketSwap): "0.2"}, baselineTargetKeys(decisions[0]))
	require.Equal(t, StatusOK, decisions[1].Status, "写了 allow_empty 的规则不参与整期跳过的检查")
	require.Empty(t, decisions[1].Targets)
	require.Empty(t, decisions[1].State.Rules["timing"].Held, "缺数的持仓出场")
	item := baselineItem(decisions[1], "timing", "A")
	require.Equal(t, StageMissing, item.Stage)
	require.Contains(t, item.Reason, reasonExitMissing)
}

// 有冻结标的时的权重：预算在全部持有标的（含冻结）之间平分，冻结的份额由组合层保持，合计不超过规则预算。
func TestHoldPoolSharesBudgetWithFrozen(t *testing.T) {
	raw := `name: frozen
rules:
  - id: timing
    type: rank
    pool: [A, B, C]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "sig < 0"
    weight: {total: 0.6}
portfolio:
  max_missing: 1
`
	// 第 1 期 A 所在市场不可用（没有行，带 market_unavailable 证据）：已持有的 A 冻结。
	down := holdPoolRows("sig", map[string]float64{"B": 1, "C": 1})
	down.Failed = map[string]map[string]map[string]string{dsl.MarketSwap: {"sig": {"A": FailedMarket}}}
	bars := []baselineEngineBar{holdPoolRows("sig", map[string]float64{"A": 1, "B": 1, "C": 1}), down}
	key := func(id string) string { return TargetKey(id, dsl.MarketSwap) }
	decisions := holdPoolRun(t, raw, []string{"sig"}, bars)
	require.Equal(t, map[string]string{key("A"): "0.2", key("B"): "0.2", key("C"): "0.2"}, baselineTargetKeys(decisions[0]))
	require.Equal(t, map[string]string{key("A"): "0.2(hold)", key("B"): "0.2", key("C"): "0.2"}, baselineTargetKeys(decisions[1]))
}

// 明细原因：入场、不满足入场、入场与出场同根成立（不入场）、出场。
func TestHoldPoolItemReasons(t *testing.T) {
	raw := `name: reasons
rules:
  - id: timing
    type: rank
    pool: [A, B, C]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "out > 0"
    weight: {total: 0.6}
portfolio:
  max_missing: 1
`
	row := func(sig, out float64) map[string]float64 { return map[string]float64{"sig": sig, "out": out} }
	bars := []baselineEngineBar{
		// 第 0 期：A 入场；B 不满足入场；C 入场与出场同根成立，不入场。
		{Rows: baselineOne(dsl.MarketSwap, map[string]map[string]float64{"A": row(1, 0), "B": row(0, 0), "C": row(1, 1)})},
		// 第 1 期：A 出场。
		{Rows: baselineOne(dsl.MarketSwap, map[string]map[string]float64{"A": row(1, 1), "B": row(0, 0), "C": row(0, 0)})},
	}
	decisions := holdPoolRun(t, raw, []string{"sig", "out"}, bars)
	require.Equal(t, map[string]string{TargetKey("A", dsl.MarketSwap): "0.6"}, baselineTargetKeys(decisions[0]))
	require.Empty(t, decisions[1].Targets)
	require.Equal(t, reasonHoldEntered, baselineItem(decisions[0], "timing", "A").Reason)
	require.Equal(t, reasonHoldNotEntered, baselineItem(decisions[0], "timing", "B").Reason)
	require.Equal(t, reasonHoldNotEntered, baselineItem(decisions[0], "timing", "C").Reason)
	require.Equal(t, reasonExitCondition, baselineItem(decisions[1], "timing", "A").Reason)
}

// holdPoolStrategy 是最小的固定标的择时策略：A、B 各自按 sig 进出场，持有的标的平分预算。
const holdPoolStrategy = `name: hold_pool
rules:
  - id: s
    type: rank
    pool: [A, B]
    score: "0"
    select: {top: 10000}
    hold:
      entry: "sig > 0"
      exit: "sig < 0"
    weight: {total: 1}
portfolio:
  max_missing: 1
`
