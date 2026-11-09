package engine

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/stretchr/testify/require"
)

// 系数截到 [0, max] 后乘在输出权重上，乘掉的部分留现金；系数为 0 的不出目标，明细记权重 0。
func TestScaleClampsAndLeavesCash(t *testing.T) {
	program := compile(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 3}
    weight: {total: 0.9}
    scale: {expr: "c", max: 1}
portfolio: {max_missing: 1}
`, "m", "c")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 3, "c", 2), "B": values("m", 2, "c", 0.5), "C": values("m", 1, "c", -1),
	}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.3", "B": "0.15"})
	require.Equal(t, "1", findItem(t, decision, "r", "A").Scale)
	require.Equal(t, "0.5", findItem(t, decision, "r", "B").Scale)
	c := findItem(t, decision, "r", "C")
	require.Equal(t, StageWeighted, c.Stage)
	require.Equal(t, "0", c.Weight)
	require.Equal(t, "0", c.Scale)
	assertDecimal(t, "cash", decision.Summary.Cash, "0.55")
	// 规则状态不含系数：系数为 0 的 C 仍在持有里。
	require.Equal(t, []string{"A", "B", "C"}, decision.State.Rules["r"].Held)
	// 不写 scale 的明细没有系数字段。
	plain := compile(t, "name: p\nrules:\n  - {id: r, type: rank, score: m, select: {top: 1}, weight: 1}\n", "m")
	require.Empty(t, findItem(t, evaluate(t, plain, frameOf(plain, map[string]Row{"A": values("m", 1)}), State{}), "r", "A").Scale)
}

// 系数为 NaN 按缺数处理（不当 0）。
func TestScaleNaNIsMissing(t *testing.T) {
	program := compile(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    weight: 1
    scale: {expr: "c"}
portfolio: {max_missing: 1}
`, "m", "c")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 3, "c", math.NaN()), "B": values("m", 2, "c", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"B": "1"})
	item := findItem(t, decision, "r", "A")
	require.Equal(t, StageMissing, item.Stage)
	require.Equal(t, reasonScaleInvalid, item.Reason)
	require.Equal(t, 1, decision.Summary.Rules["r"].Missing)
}

// holding 批次乘系数，批次的基准权重不变；signal 规则同样乘在输出权重上。
func TestScaleOnHoldingBatchesAndSignal(t *testing.T) {
	program := compile(t, `name: s
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
    holding: {bars: 2, offsets: [0]}
    scale: {expr: "c", max: 2}
  - id: g
    type: signal
    pool: [B]
    entry: "m > 0"
    exit: "m < 0"
    weight: 0.4
    scale: {expr: "c", max: 2}
portfolio: {max_missing: 1, leverage: 2}
`, "m", "c")
	frame := frameOf(program, map[string]Row{"A": values("m", 3, "c", 1.5), "B": values("m", 1, "c", 0.25)})
	frame.BarIndex, frame.Expected["g"] = 10, []string{"B"}
	first := evaluate(t, program, frame, State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"A": "0.75", "B": "0.1"})
	require.Equal(t, map[string]string{"A": "1"}, first.State.Rules["r"].Batches[0].BaseWeights)
	frame = frameOf(program, map[string]Row{"A": values("m", 3, "c", 0.5), "B": values("m", 1, "c", 2)})
	frame.BarIndex, frame.Expected["g"] = 11, []string{"B"}
	second := evaluate(t, program, frame, first.State)
	assertWeights(t, second, map[string]string{"A": "0.25", "B": "0.8"})
	require.Equal(t, map[string]string{"A": "1"}, second.State.Rules["r"].Batches[0].BaseWeights)
}

const onMissingStrategy = `name: o
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
    on_missing: {keep_bars: 2}
  - id: q
    type: rank
    score: "n"
    select: {top: 1}
    weight: 0.5
portfolio: {max_missing: 1}
`

// on_missing：持有标的本根缺数时冻结保持（missing_kept），连续缺数不超过 keep_bars；超过后按现状移出；数据恢复后计数清零；
// 停用再启用后计数从零开始。没持有它的规则 q 不阻止冻结。
func TestOnMissingKeepsThenReleases(t *testing.T) {
	program := compile(t, onMissingStrategy, "m", "n")
	step := func(rows map[string]Row, state State) Decision {
		t.Helper()
		decision := evaluate(t, program, frameOf(program, rows), state)
		assertOK(t, decision)
		return decision
	}
	full := map[string]Row{"A": values("m", 2, "n", 1), "B": values("m", 1, "n", 2)}
	gap := map[string]Row{"A": values("n", 1), "B": values("m", 1, "n", 2)}
	first := step(full, State{})
	assertWeights(t, first, map[string]string{"A": "0.5", "B": "0.5"})
	second := step(gap, first.State)
	require.Equal(t, StageHeld, findItem(t, second, "r", "A").Stage)
	require.Equal(t, reasonMissingKept, findItem(t, second, "r", "A").Reason)
	require.True(t, holdsTarget(second, "A"))
	require.Equal(t, map[string]int{"A": 1}, second.State.Rules["r"].MissingStreak)
	// 数据恢复：计数清零，照常求值。
	recovered := step(full, second.State)
	require.Empty(t, recovered.State.Rules["r"].MissingStreak)
	// 连续缺数 2 根仍保持，第 3 根超过 keep_bars 移出。
	third := step(gap, recovered.State)
	fourth := step(gap, third.State)
	require.Equal(t, map[string]int{"A": 2}, fourth.State.Rules["r"].MissingStreak)
	require.True(t, holdsTarget(fourth, "A"))
	fifth := step(gap, fourth.State)
	require.Equal(t, StageMissing, findItem(t, fifth, "r", "A").Stage)
	require.False(t, holdsTarget(fifth, "A"))
	require.Empty(t, fifth.State.Rules["r"].MissingStreak)
	// 停用再启用：从零开始，前序状态为空时 A 不是持仓，不冻结。
	fresh := step(gap, State{})
	require.Empty(t, fresh.State.Rules["r"].MissingStreak)
}

// 持有同一标的、但没写 on_missing 的规则会阻止冻结。
func TestOnMissingBlockedByHolderWithout(t *testing.T) {
	program := compile(t, `name: o
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
    on_missing: {keep_bars: 3}
  - id: q
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
portfolio: {max_missing: 1}
`, "m")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 2), "B": values("m", 1)}), State{})
	assertWeights(t, first, map[string]string{"A": "1"})
	second := evaluate(t, program, frameOf(program, map[string]Row{"A": values("n", 1), "B": values("m", 1)}), first.State)
	assertOK(t, second)
	require.Equal(t, StageMissing, findItem(t, second, "r", "A").Stage)
	require.False(t, holdsTarget(second, "A"))
}

// signal 规则写 on_missing：持仓的 exit 输入缺失时冻结保持，不清仓。
func TestOnMissingSignal(t *testing.T) {
	program := compile(t, `name: o
rules:
  - id: g
    type: signal
    pool: [A]
    entry: "m > 0"
    exit: "m < 0"
    weight: 1
    on_missing: {keep_bars: 1}
portfolio: {max_missing: 1}
`, "m")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 1)}), State{})
	assertWeights(t, first, map[string]string{"A": "1"})
	second := evaluate(t, program, frameOf(program, map[string]Row{"A": values("x", 1)}), first.State)
	require.True(t, holdsTarget(second, "A"))
	require.Equal(t, []string{"A"}, second.State.Rules["g"].Held)
	third := evaluate(t, program, frameOf(program, map[string]Row{"A": values("x", 1)}), second.State)
	require.False(t, holdsTarget(third, "A"))
}

// tranche 规则的 scale：在 tranche 配好的权重上乘系数，开仓记录不变；scale 引用的列计入 tranche 的列。
func TestScaleOnTranche(t *testing.T) {
	bars := loadTrancheBars(t)
	golden := loadGoldenCase(t, "multi_sub_merge")
	strategy, err := dsl.Parse([]byte(golden.DSL))
	require.NoError(t, err)
	scaled := strategy
	scaled.Rules = append([]dsl.Rule(nil), strategy.Rules...)
	scaled.Rules[0].Scale = &dsl.Scale{Expr: "close * 0 + 0.5", Max: 1}
	plain, err := compileMode(strategy, columnsOf(bars))
	require.NoError(t, err)
	program, err := compileMode(scaled, columnsOf(bars))
	require.NoError(t, err)
	require.Equal(t, plain.Rules[0].Columns(), program.Rules[0].Columns())
	var before, after RuleState
	half := quant.Must("0.5")
	for i, bar := range bars {
		frame := trancheFrame(bar, int64(i), program.Rules[0].Rule.ID)
		a, err := evaluateTranche(&plain.Rules[0], frame, partitionTranche(&plain.Rules[0], frame, before.Held), before, noise)
		require.NoError(t, err)
		b, err := evaluateTranche(&program.Rules[0], frame, partitionTranche(&program.Rules[0], frame, after.Held), after, noise)
		require.NoError(t, err)
		applyScale(&program.Rules[0], frame, &b, noise)
		for id, w := range a.weights {
			require.Zero(t, w.Mul(half).Cmp(b.weights[id]), "第 %d 期 %s", i, id)
		}
		before, after = a.state, b.state
	}
}

// holding 批次写 on_missing：成员本根缺数时冻结保持，份额留在批次里，恢复后继续。
func TestOnMissingHoldingBatch(t *testing.T) {
	program := compile(t, `name: o
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: 1
    holding: {bars: 3, offsets: [0]}
    on_missing: {keep_bars: 2}
portfolio: {max_missing: 1}
`, "m")
	frame := frameOf(program, map[string]Row{"A": values("m", 2), "B": values("m", 1)})
	frame.BarIndex = 9
	first := evaluate(t, program, frame, State{})
	assertWeights(t, first, map[string]string{"A": "1"})
	gap := frameOf(program, map[string]Row{"A": values("x", 1), "B": values("m", 1)})
	gap.BarIndex = 10
	second := evaluate(t, program, gap, first.State)
	require.True(t, holdsTarget(second, "A"))
	require.Equal(t, map[string]string{"A": "1"}, second.State.Rules["r"].Batches[0].BaseWeights)
	back := frameOf(program, map[string]Row{"A": values("m", 2), "B": values("m", 1)})
	back.BarIndex = 11
	third := evaluate(t, program, back, second.State)
	assertWeights(t, third, map[string]string{"A": "1"})
	require.Empty(t, third.State.Rules["r"].MissingStreak)
}
