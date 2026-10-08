package engine

import (
	"testing"
)

const missingStrategy = `name: s1
universe:
  exclude: [BTC-USDT]
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 10}
    weight: {total: 0.5}
  - id: btc
    type: signal
    pool: [BTC-USDT]
    entry: "m > 0"
    exit: "m < 0"
    weight: {total: 0.5}
portfolio:
  max_missing: 0.2
`

// S1：E(r) 有 100 个标的、40 个没有行时触发 too_many_missing；年龄剔除不计入缺数；
// universe 排除的 BTC 仍出现在显式 pool 规则的 E(r) 中并被读取。
func TestS1MissingGuardCountsOnlyExpectedMinusAgedOut(t *testing.T) {
	program := compile(t, missingStrategy, "m")
	ids := numberedIDs("ALT", 100)
	build := func(missing, aged int) Frame {
		rows := map[string]Row{"BTC-USDT": values("m", 1)}
		for i, id := range ids {
			if i < missing {
				continue
			}
			rows[id] = values("m", i)
		}
		frame := frameOf(program, rows)
		frame.Expected["r"] = append([]string(nil), ids...)
		frame.Expected["btc"] = []string{"BTC-USDT"}
		// 年龄剔除的标的取缺行之后的那一段。
		frame.AgedOut["r"] = ids[missing : missing+aged]
		frame.Universe = append(append([]string(nil), ids...), "BTC-USDT")
		return frame
	}

	previous := State{Rules: map[string]RuleState{"r": {Held: ids[:3]}}}
	skipped := evaluate(t, program, build(40, 0), previous)
	assertSkipped(t, skipped, SkipTooManyMissing)
	if summary := skipped.Summary.Rules["r"]; summary.Expected != 100 || summary.Missing != 40 || summary.Available != 60 {
		t.Fatalf("摘要不符：%+v", summary)
	}
	if countItems(skipped, "r", StageMissing) != 40 || findItem(t, skipped, "r", ids[0]).Reason != "no_row" {
		t.Fatalf("缺数明细不符：%d", countItems(skipped, "r", StageMissing))
	}
	if len(skipped.State.Rules["r"].Held) != 3 {
		t.Fatalf("跳过时规则状态应沿用前序：%+v", skipped.State)
	}

	// 20 个年龄剔除 + 16 个缺行：16/80 = 0.2，不超过阈值。
	boundary := evaluate(t, program, build(16, 20), State{})
	assertOK(t, boundary)
	if summary := boundary.Summary.Rules["r"]; summary.AgedOut != 20 || summary.Missing != 16 || summary.Available != 64 {
		t.Fatalf("摘要不符：%+v", summary)
	}
	if countItems(boundary, "r", StageAgedOut) != 20 {
		t.Fatalf("年龄剔除明细应为 20：%d", countItems(boundary, "r", StageAgedOut))
	}
	// 17/80 > 0.2。
	assertSkipped(t, evaluate(t, program, build(17, 20), State{}), SkipTooManyMissing)

	// 显式 pool 的 BTC 不受 universe.exclude 影响。
	if item := findItem(t, boundary, "btc", "BTC-USDT"); item.Stage != StageWeighted || item.Weight != "0.5" {
		t.Fatalf("BTC 应在 signal 规则中进入权重：%+v", item)
	}
	for _, item := range boundary.Items {
		if item.RuleID == "r" && item.InstrumentID == "BTC-USDT" {
			t.Fatalf("rank 规则不应读取 universe 排除的 BTC：%+v", item)
		}
	}
	if boundary.Summary.Universe != 101 {
		t.Fatalf("|U| 应为 101：%d", boundary.Summary.Universe)
	}
}

// S2：某因子只对规则 r1 失败，r1 的候选剔除该标的，r2 不受影响。
func TestS2FactorFailureOnlyAffectsReferencingRule(t *testing.T) {
	program := compile(t, `name: s2
rules:
  - id: r1
    type: rank
    score: "f1"
    select: {top: 2}
    weight: {total: 0.5}
  - id: r2
    type: rank
    score: "f2"
    select: {top: 2}
    weight: {total: 0.5}
portfolio:
  max_missing: 1
`, "f1", "f2")
	frame := frameOf(program, map[string]Row{"X": values("f1", 1, "f2", 1), "Y": values("f1", 2, "f2", 2)})
	frame.FailedColumns = map[string]map[string]struct{}{"f1": {"X": {}}}
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"X": "0.25", "Y": "0.75"})
	if item := findItem(t, decision, "r1", "X"); item.Stage != StageMissing || item.Reason != "factor_failed:f1" {
		t.Fatalf("r1 下 X 应记为因子失败：%+v", item)
	}
	if item := findItem(t, decision, "r2", "X"); item.Stage != StageWeighted {
		t.Fatalf("r2 下 X 应进入权重：%+v", item)
	}
	if decision.Summary.Rules["r1"].Missing != 1 || decision.Summary.Rules["r2"].Missing != 0 {
		t.Fatalf("摘要不符：%+v / %+v", decision.Summary.Rules["r1"], decision.Summary.Rules["r2"])
	}
}

// bars[-1] 引用的上一根列缺失也算缺数。
func TestPreviousBarColumnMissingCountsAsMissing(t *testing.T) {
	program := compile(t, `name: prev
rules:
  - id: r
    type: rank
    score: "close - bars[-1].close"
    select: {top: 2}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "close")
	frame := frameOf(program, map[string]Row{
		"A": {Values: map[string]float64{"close": 2}, Previous: map[string]float64{"close": 1}},
		"B": {Values: map[string]float64{"close": 2}},
	})
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "1"})
	if item := findItem(t, decision, "r", "B"); item.Stage != StageMissing || item.Reason != "missing:bars[-1].close" {
		t.Fatalf("B 应记为上一根缺数：%+v", item)
	}
}
