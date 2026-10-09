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
	frame.FailedColumns = map[string]map[string]string{"f1": {"X": FailedFactor}}
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

// 守门边界：预期集合为空或全部年龄剔除按 universe_too_small 跳过；基础集合为空按 no_data 跳过；
// 显式 max_missing: 0 时一个缺数即跳过。跳过时状态沿用前序。
func TestGuardEdgeCases(t *testing.T) {
	program := compile(t, `name: edge
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
`, "m")
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}}}, Targets: map[string]string{"A": "1"}}

	emptyPool := frameOf(program, map[string]Row{"A": values("m", 1)})
	emptyPool.Expected["r"] = nil
	decision := evaluate(t, program, emptyPool, previous)
	assertSkipped(t, decision, SkipUniverseTooSmall)
	if decision.State.Targets["A"] != "1" {
		t.Fatalf("跳过时状态应沿用前序：%+v", decision.State)
	}

	allAged := frameOf(program, map[string]Row{"A": values("m", 1), "B": values("m", 2)})
	allAged.AgedOut = map[string][]string{"r": {"A", "B"}}
	assertSkipped(t, evaluate(t, program, allAged, State{}), SkipUniverseTooSmall)
	// 上期持有的 A 在建仓时已满足年龄要求，探针窗口内的数据缺口不能把它当作新上市剔除。
	kept := evaluate(t, program, allAged, previous)
	assertOK(t, kept)
	assertWeights(t, kept, map[string]string{"A": "1"})
	if item := findItem(t, kept, "r", "B"); item.Stage != StageAgedOut {
		t.Fatalf("未持有的 B 仍按年龄剔除：%+v", item)
	}

	noData := frameOf(program, map[string]Row{})
	decision = evaluate(t, program, noData, previous)
	assertSkipped(t, decision, SkipNoData)
	if decision.State.Rules["r"].Held[0] != "A" {
		t.Fatalf("no_data 时规则状态应沿用前序：%+v", decision.State)
	}

	strict := compile(t, `name: strict
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 0
`, "m")
	oneMissing := frameOf(strict, map[string]Row{"A": values("m", 1), "B": values("m", 2)})
	oneMissing.Expected["r"] = []string{"A", "B", "C"}
	assertSkipped(t, evaluate(t, strict, oneMissing, State{}), SkipTooManyMissing)
}

// 上期持有、本期已不在预期集合中的标的给出 dropped(not_expected) 明细。
func TestHeldInstrumentLeavingExpectedSetIsExplained(t *testing.T) {
	signal := compile(t, `name: gone
rules:
  - id: s
    type: signal
    pool: [BTC-USDT, ETH-USDT]
    entry: "m > 0"
    exit: "m < 0"
    weight: {total: 0.5}
`, "m")
	frame := frameOf(signal, map[string]Row{"ETH-USDT": values("m", 1)})
	previous := State{Rules: map[string]RuleState{"s": {Held: []string{"BTC-USDT"}}}, Targets: map[string]string{"BTC-USDT": "0.5"}}
	decision := evaluate(t, signal, frame, previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"ETH-USDT": "0.5"})
	if item := findItem(t, decision, "s", "BTC-USDT"); item.Stage != StageDropped || item.Reason != "not_expected" {
		t.Fatalf("离开预期集合的 BTC 应记 not_expected：%+v", item)
	}

	ranked := compile(t, bufferStrategy, "m")
	frame = frameOf(ranked, map[string]Row{"A": values("m", 3), "B": values("m", 2), "C": values("m", 1)})
	decision = evaluate(t, ranked, frame, State{Rules: map[string]RuleState{"r": {Held: []string{"A", "Z"}}}})
	assertOK(t, decision)
	if item := findItem(t, decision, "r", "Z"); item.Stage != StageDropped || item.Reason != "not_expected" {
		t.Fatalf("离开预期集合的 Z 应记 not_expected：%+v", item)
	}
	if countItems(decision, "r", StageWeighted) != 2 {
		t.Fatalf("A、B 应入选：%+v", decision.Items)
	}
}
