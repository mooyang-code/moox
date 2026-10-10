package engine

import (
	"strings"
	"testing"
)

const signalStrategy = `name: signal
rules:
  - id: s
    type: signal
    pool: [A, B]
    entry: "sig > 0"
    exit: "sig < 0"
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// signal：exit 优先于 entry，持有标的缺数视为退出（缺数比例未超过 max_missing 时），持有标的平分预算。
func TestSignalExitPriorityAndMissingExit(t *testing.T) {
	program := compile(t, signalStrategy, "sig")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 0)}), State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"A": "1"})
	if item := findItem(t, first, "s", "B"); item.Stage != StageIdle || item.Reason != "no_entry" {
		t.Fatalf("B 应为未触发：%+v", item)
	}
	if held := first.State.Rules["s"].Held; len(held) != 1 || held[0] != "A" {
		t.Fatalf("理论持仓应为 A：%v", held)
	}

	second := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", -1), "B": values("sig", 1)}), first.State)
	assertOK(t, second)
	assertWeights(t, second, map[string]string{"B": "1"})
	if item := findItem(t, second, "s", "A"); item.Stage != StageDropped || item.Reason != "exit" {
		t.Fatalf("A 应退出：%+v", item)
	}

	third := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 0), "B": values("other", 1)}), second.State)
	assertOK(t, third)
	if len(third.Targets) != 0 {
		t.Fatalf("持有标的缺数应清仓：%v", third.Targets)
	}
	if item := findItem(t, third, "s", "B"); item.Stage != StageMissing || item.Reason != "missing:sig" {
		t.Fatalf("B 应记为缺数：%+v", item)
	}
	assertDecimal(t, "cash", third.Summary.Cash, "1")
	if len(third.State.Rules["s"].Held) != 0 {
		t.Fatalf("理论持仓应清空：%v", third.State.Rules["s"])
	}

	// 两个标的同时持有时平分预算。
	both := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 1)}), State{})
	assertWeights(t, both, map[string]string{"A": "0.5", "B": "0.5"})
}

// entry 与 exit 同时为真时不入场。
func TestSignalEntryAndExitTogetherStaysIdle(t *testing.T) {
	program := compile(t, `name: both
rules:
  - id: s
    type: signal
    pool: [A]
    entry: "a > 0"
    exit: "b > 0"
    weight: {total: 1}
`, "a", "b")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("a", 1, "b", 1)}), State{})
	assertOK(t, decision)
	if len(decision.Targets) != 0 {
		t.Fatalf("不应入场：%v", decision.Targets)
	}
	if item := findItem(t, decision, "s", "A"); item.Stage != StageIdle || item.Reason != "entry_and_exit" {
		t.Fatalf("A 应记为 entry_and_exit：%+v", item)
	}
}

// signal 规则的池是显式的，不受 min_universe 约束。
func TestSignalIgnoresMinUniverse(t *testing.T) {
	program := compile(t, `name: nomin
rules:
  - id: s
    type: signal
    pool: [A]
    entry: "sig > 0"
    exit: "sig < 0"
    weight: {total: 1}
portfolio:
  min_universe: 5
`, "sig")
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1)}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "1"})
}

// signal 规则同样按 max_missing 守门：池中缺数比例过高时整期跳过、沿用前序状态，不能把大面积缺数当作退出清仓。
func TestSignalTooManyMissingSkips(t *testing.T) {
	program := compile(t, strings.Replace(signalStrategy, "max_missing: 1", "max_missing: 0.2", 1), "sig")
	previous := State{Rules: map[string]RuleState{"s": {Held: []string{"A"}}}, Targets: map[string]string{"A": "1"}}
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("other", 1), "B": values("sig", 1)}), previous)
	assertSkipped(t, decision, SkipTooManyMissing)
	if decision.State.Rules["s"].Held[0] != "A" {
		t.Fatalf("跳过时应沿用前序持仓：%+v", decision.State)
	}
}
