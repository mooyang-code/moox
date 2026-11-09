package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const allowEmptyStrategy = `name: e
rules:
  - id: main
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
  - id: fresh
    type: rank
    score: "n"
    select: {top: 1}
    weight: 0.5
    allow_empty: true
    universe: {max_age_bars: 30}
portfolio: {min_universe: 1, max_missing: 0.2}
`

// allow_empty：规则候选全部被年龄剔除（超过上限）、或大面积缺数时，策略不整期跳过，这条规则不出目标、预算留现金；
// 其他规则照常守门。
func TestAllowEmptyRuleSkipsGuards(t *testing.T) {
	program := compile(t, allowEmptyStrategy, "m", "n")
	rows := map[string]Row{"A": values("m", 2, "n", 1), "B": values("m", 1, "n", 2)}
	// fresh 的候选都已通过 30 根判定（不是新标的）：全部记 aged_out_max，预期 − 年龄剔除 = 0。
	frame := frameOf(program, rows)
	frame.AgedOut["fresh"] = []string{"A", "B"}
	frame.AgedOutMax = map[string][]string{"fresh": {"A", "B"}}
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.5"})
	require.Equal(t, StageAgedOut, findItem(t, decision, "fresh", "A").Stage)
	require.Equal(t, reasonAgedOutMax, findItem(t, decision, "fresh", "A").Reason)
	assertDecimal(t, "cash", decision.Summary.Cash, "0.5")

	// fresh 的候选全部缺数（缺数占比 100%）：仍不守门，缺数的只是不入选。
	missing := frameOf(program, map[string]Row{"A": values("m", 2), "B": values("m", 1)})
	decision = evaluate(t, program, missing, State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.5"})
	require.Equal(t, StageMissing, findItem(t, decision, "fresh", "B").Stage)

	// 不写 allow_empty 的规则照常守门：main 全部缺数、可用标的少于 min_universe 时整期跳过。
	broken := frameOf(program, map[string]Row{"A": values("n", 1), "B": values("n", 2)})
	assertSkipped(t, evaluate(t, program, broken, State{}), SkipUniverseTooSmall)
}

// allow_empty 为假时（现状）：规则的候选全部年龄剔除就整期 universe_too_small。
func TestWithoutAllowEmptyGuardStillApplies(t *testing.T) {
	program := compile(t, `name: e
universe: {min_age_bars: 3}
rules:
  - id: main
    type: rank
    score: "m"
    select: {top: 1}
    weight: 1
portfolio: {max_missing: 1}
`, "m")
	frame := frameOf(program, map[string]Row{"A": values("m", 2)})
	frame.AgedOut["main"] = []string{"A"}
	decision := evaluate(t, program, frame, State{})
	assertSkipped(t, decision, SkipUniverseTooSmall)
	require.Equal(t, "min_age_bars", findItem(t, decision, "main", "A").Reason)
}

// allow_empty 规则的分数对全部候选都无效（例如只有 1 个候选时 n/(n-1) 除零）：只清空这条规则，预算留现金，不整期 config_error；
// hold 规则同样。没写 allow_empty 的规则仍整期跳过。
func TestAllowEmptyInvalidScoresOnlyEmptiesRule(t *testing.T) {
	for _, extra := range []string{"", "\n    hold: {}"} {
		program := compile(t, `name: e
rules:
  - id: main
    type: rank
    score: "m"
    select: {top: 1}
    weight: 0.5
  - id: fresh
    type: rank
    score: "m / (n - 1)"
    select: {top: 1}
    weight: 0.5
    allow_empty: true`+extra+`
portfolio: {max_missing: 1}
`, "m", "n")
		decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 2, "n", 1)}), State{})
		assertOK(t, decision)
		assertWeights(t, decision, map[string]string{"A": "0.5"})
		require.Equal(t, StageDropped, findItem(t, decision, "fresh", "A").Stage)
		assertDecimal(t, "cash", decision.Summary.Cash, "0.5")
	}
	strict := compile(t, "name: s\nrules:\n  - {id: r, type: rank, score: \"m / (n - 1)\", select: {top: 1}, weight: 1}\nportfolio: {max_missing: 1}\n", "m", "n")
	assertSkipped(t, evaluate(t, strict, frameOf(strict, map[string]Row{"A": values("m", 2, "n", 1)}), State{}), SkipConfigError)
}
