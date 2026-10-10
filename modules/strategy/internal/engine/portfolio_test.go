package engine

import (
	"strings"
	"testing"
)

// S5：两条规则选中同一标的、合计超过 portfolio.max_weight：超出留现金，gross 不超过 leverage。
func TestS5MaxWeightExcessBecomesCash(t *testing.T) {
	program := compile(t, `name: s5
rules:
  - id: a
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
  - id: b
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.5}
portfolio:
  leverage: 1
  max_weight: 0.6
`, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"X": values("m", 2), "Y": values("m", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"X": "0.6"})
	assertDecimal(t, "gross", decision.Summary.Gross, "0.6")
	assertDecimal(t, "cash", decision.Summary.Cash, "0.4")
	if len(decision.Summary.Notes) != 1 || !strings.Contains(decision.Summary.Notes[0], "max_weight") {
		t.Fatalf("应记录裁剪说明：%v", decision.Summary.Notes)
	}
	if decision.State.Targets["X"] != "0.6" {
		t.Fatalf("状态中的目标应为裁剪后的值：%v", decision.State.Targets)
	}
}

const leveredStrategy = `name: s21
rules:
  - id: a
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.6}
  - id: b
    type: rank
    score: "n"
    select: {top: 1}
    weight: {total: 0.6}
portfolio:
  leverage: 1.2
`

// S21（引擎部分）：现货 View 上合成 Σw > 1 则 skipped(config_error)；合约 View 允许。
func TestS21SpotRejectsWeightSumAboveOne(t *testing.T) {
	program := compile(t, leveredStrategy, "m", "n")
	frame := frameOf(program, map[string]Row{"X": values("m", 2, "n", 1), "Y": values("m", 1, "n", 2)})
	frame.Spot = true
	spot := evaluate(t, program, frame, State{})
	assertSkipped(t, spot, SkipConfigError)
	if len(spot.Summary.Notes) == 0 || !strings.Contains(spot.Summary.Notes[0], "超过 1") {
		t.Fatalf("应说明原因：%v", spot.Summary.Notes)
	}
	frame.Spot = false
	swap := evaluate(t, program, frame, State{})
	assertOK(t, swap)
	assertWeights(t, swap, map[string]string{"X": "0.6", "Y": "0.6"})
	assertDecimal(t, "gross", swap.Summary.Gross, "1.2")
	assertDecimal(t, "cash", swap.Summary.Cash, "-0.2")
}

// 做空取负：现货拒绝负权重，合约 gross 按绝对值计算。
func TestShortSideNegativeWeights(t *testing.T) {
	program := compile(t, `name: short
rules:
  - id: s
    type: rank
    score: "m"
    select: {bottom: 1}
    weight: {total: 0.5}
    side: short
`, "m")
	frame := frameOf(program, map[string]Row{"X": values("m", 2), "Y": values("m", 1)})
	frame.Spot = true
	assertSkipped(t, evaluate(t, program, frame, State{}), SkipConfigError)
	frame.Spot = false
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"Y": "-0.5"})
	assertDecimal(t, "gross", decision.Summary.Gross, "0.5")
	assertDecimal(t, "net", decision.Summary.Net, "-0.5")
	if item := findItem(t, decision, "s", "Y"); item.Weight != "-0.5" {
		t.Fatalf("解释中的权重应为负：%+v", item)
	}
}

// 换手按与上期理论目标的差计算：Σ|Δw| / 2。
func TestTurnoverAgainstPreviousTargets(t *testing.T) {
	program := compile(t, `name: turnover
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    weight: {total: 0.5}
`, "m")
	previous := State{Targets: map[string]string{"X": "0.5"}}
	decision := evaluate(t, program, frameOf(program, map[string]Row{"X": values("m", 1), "Y": values("m", 2)}), previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"X": "0.25", "Y": "0.25"})
	assertDecimal(t, "turnover", decision.Summary.Turnover, "0.25")
	first := evaluate(t, program, frameOf(program, map[string]Row{"X": values("m", 1), "Y": values("m", 2)}), State{})
	assertDecimal(t, "首期换手", first.Summary.Turnover, "0.25")
}
