package engine

import (
	"testing"
)

// gapFailed 构造「标的在某列上因 K 线空洞失败」的失败证据。
func gapFailed(column string, ids ...string) map[string]map[string]string {
	failed := map[string]map[string]string{column: {}}
	for _, id := range ids {
		failed[column][id] = FailedGap
	}
	return failed
}

func holdOf(decision Decision) map[string]bool {
	out := map[string]bool{}
	for _, target := range decision.Targets {
		if target.Hold {
			out[target.InstrumentID] = true
		}
	}
	return out
}

// signal：已持有的标的因空洞缺数时保持持仓（不判 exit、不清仓），预算先预留给它，其余标的在剩余额度里按比例缩小；
// 空洞消失后照常延续。
func TestSignalHoldsFrozenPositionAndReservesBudget(t *testing.T) {
	program := compile(t, signalStrategy, "sig")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 1)}), State{})
	assertWeights(t, first, map[string]string{"A": "0.5", "B": "0.5"})

	gap := frameOf(program, map[string]Row{"A": {Values: map[string]float64{}}, "B": values("sig", 1)})
	gap.FailedColumns = gapFailed("sig", "A")
	second := evaluate(t, program, gap, first.State)
	assertOK(t, second)
	// A 保持（预留 0.5），B 原本平分到 1，被缩到剩余的 0.5。
	assertWeights(t, second, map[string]string{"A": "0.5", "B": "0.5"})
	if holds := holdOf(second); len(holds) != 1 || !holds["A"] {
		t.Fatalf("只有 A 应是保持目标：%v", second.Targets)
	}
	if item := findItem(t, second, "s", "A"); item.Stage != StageHeld || item.Reason != reasonGapHold {
		t.Fatalf("A 应记为保持：%+v", item)
	}
	if held := second.State.Rules["s"].Held; len(held) != 2 || held[0] != "A" || held[1] != "B" {
		t.Fatalf("理论持仓应仍包含 A：%v", held)
	}
	if second.State.Targets["A"] != "0.5" {
		t.Fatalf("保持标的的预留权重要写回状态，下一期才能继续保持：%v", second.State.Targets)
	}
	if second.Summary.Rules["s"].Held != 1 || second.Summary.Rules["s"].Missing != 0 {
		t.Fatalf("摘要不符：%+v", second.Summary.Rules["s"])
	}

	// 连续多期保持：状态沿用，仍保持 A。
	third := evaluate(t, program, gap, second.State)
	assertWeights(t, third, map[string]string{"A": "0.5", "B": "0.5"})
	if !holdOf(third)["A"] {
		t.Fatalf("连续保持：%v", third.Targets)
	}

	// 空洞消失：A 正常求值，没有保持目标。
	back := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 1)}), third.State)
	assertWeights(t, back, map[string]string{"A": "0.5", "B": "0.5"})
	if len(holdOf(back)) != 0 {
		t.Fatalf("空洞消失后不应再保持：%v", back.Targets)
	}
}

// 没有行但有空洞证据的已持仓标的同样保持；没有证据的无行按原有逻辑处理（缺数、清仓）。
func TestFreezeNoRowRequiresGapEvidence(t *testing.T) {
	program := compile(t, signalStrategy, "sig")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 1)}), State{})

	withEvidence := frameOf(program, map[string]Row{"B": values("sig", 1)})
	withEvidence.Expected["s"] = []string{"A", "B"}
	withEvidence.FailedColumns = gapFailed("sig", "A")
	held := evaluate(t, program, withEvidence, first.State)
	if !holdOf(held)["A"] {
		t.Fatalf("有空洞证据的无行应保持：%v", held.Targets)
	}

	noEvidence := frameOf(program, map[string]Row{"B": values("sig", 1)})
	noEvidence.Expected["s"] = []string{"A", "B"}
	cleared := evaluate(t, program, noEvidence, first.State)
	if len(holdOf(cleared)) != 0 {
		t.Fatalf("没有证据的无行不应保持：%v", cleared.Targets)
	}
	if item := findItem(t, cleared, "s", "A"); item.Stage != StageMissing || item.Reason != "no_row" {
		t.Fatalf("A 应记为无行缺数：%+v", item)
	}
}

// 失败原因混有因子失败（非空洞）时完全按原有逻辑处理：缺数的持仓被清仓。
func TestFreezeMixedCausesFallBackToExit(t *testing.T) {
	program := compile(t, signalStrategy, "sig")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 1)}), State{})
	frame := frameOf(program, map[string]Row{"A": {Values: map[string]float64{}}, "B": values("sig", 1)})
	frame.FailedColumns = map[string]map[string]string{"sig": {"A": FailedFactor}}
	second := evaluate(t, program, frame, first.State)
	assertOK(t, second)
	assertWeights(t, second, map[string]string{"B": "1"})
	if len(holdOf(second)) != 0 {
		t.Fatalf("因子失败不应保持：%v", second.Targets)
	}
}

// 没有持仓的标的因空洞缺数只是缺数，不会凭空产生保持目标。
func TestFreezeOnlyAppliesToHeldInstruments(t *testing.T) {
	program := compile(t, signalStrategy, "sig")
	frame := frameOf(program, map[string]Row{"A": {Values: map[string]float64{}}, "B": values("sig", 1)})
	frame.FailedColumns = gapFailed("sig", "A")
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"B": "1"})
	if item := findItem(t, decision, "s", "A"); item.Stage != StageMissing || item.Reason != "factor_gap:sig" {
		t.Fatalf("A 应记为空洞缺数：%+v", item)
	}
}

// 已持有标的当期输入齐全、只缺上一根（bars[-1]）时保持，而不是按 exit 输入缺失清仓。
func TestFreezePreviousBarOnly(t *testing.T) {
	program := compile(t, `name: prev
rules:
  - id: s
    type: signal
    pool: [A, B]
    entry: "sig > 0"
    exit: "bars[-1].sig > 0 && bars[0].sig < 0"
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "sig")
	withPrevious := func(value float64) Row {
		return Row{Values: map[string]float64{"sig": value}, Previous: map[string]float64{"sig": value}}
	}
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": withPrevious(1), "B": withPrevious(1)}), State{})
	assertWeights(t, first, map[string]string{"A": "0.5", "B": "0.5"})
	rows := map[string]Row{
		"A": {Values: map[string]float64{"sig": 1}, Previous: map[string]float64{}},
		"B": {Values: map[string]float64{"sig": 1}, Previous: map[string]float64{"sig": 1}},
	}
	second := evaluate(t, program, frameOf(program, rows), first.State)
	assertOK(t, second)
	if !holdOf(second)["A"] {
		t.Fatalf("只缺上一根的持仓应保持：%v", second.Targets)
	}
}

// 冻结的标的仍计入缺数比例：保护中的标的不能伪装成可用，大面积空洞照样整期跳过。
func TestFreezeCountsTowardMissingGuard(t *testing.T) {
	program := compile(t, `name: guard
rules:
  - id: s
    type: signal
    pool: [A, B]
    entry: "sig > 0"
    exit: "sig < 0"
    weight: {total: 1}
portfolio:
  max_missing: 0.4
`, "sig")
	first := evaluate(t, program, frameOf(program, map[string]Row{"A": values("sig", 1), "B": values("sig", 1)}), State{})
	frame := frameOf(program, map[string]Row{"A": {Values: map[string]float64{}}, "B": values("sig", 1)})
	frame.FailedColumns = gapFailed("sig", "A")
	second := evaluate(t, program, frame, first.State)
	assertSkipped(t, second, SkipTooManyMissing)
}

// rank：冻结标的不参与排名，剩余标的在剩余预算内按比例缩小；预留耗尽（R ≥ B）时其余目标为零。
func TestRankFreezeScalesRemainingTargets(t *testing.T) {
	program := compile(t, bufferStrategy, "m")
	first := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 3), "B": values("m", 2), "C": values("m", 1), "D": values("m", 0.5),
	}), State{})
	assertWeights(t, first, map[string]string{"A": "0.4", "B": "0.4"})

	frame := frameOf(program, map[string]Row{
		"A": {Values: map[string]float64{}}, "B": values("m", 2), "C": values("m", 3), "D": values("m", 1),
	})
	frame.FailedColumns = gapFailed("m", "A")
	second := evaluate(t, program, frame, first.State)
	assertOK(t, second)
	// A 保持 0.4；B、C 原本各 0.4（共 0.8），剩余预算 0.6，缩小为各 0.3。
	assertWeights(t, second, map[string]string{"A": "0.4", "B": "0.3", "C": "0.3"})
	assertDecimal(t, "gross", second.Summary.Gross, "1")
	if held := second.State.Rules["r"].Held; len(held) != 3 {
		t.Fatalf("buffer 状态应保留冻结的 A：%v", held)
	}

	// 预留耗尽：把上期目标改成占满预算，其余目标为零。
	full := State{Rules: first.State.Rules, Targets: map[string]string{"A": "1"}}
	exhausted := evaluate(t, program, frame, full)
	assertWeights(t, exhausted, map[string]string{"A": "1"})
}

const holdingStrategy = `name: hold
rules:
  - id: h
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.8}
    holding: {bars: 2, offsets: [0, 1]}
portfolio:
  max_missing: 1
`

// holding：含冻结成员的批次延后重建，期间保持它的份额；保持解除后补建，节奏仍按 offset 对齐。
func TestHoldingDelaysRebuildWhileMemberFrozen(t *testing.T) {
	program := compile(t, holdingStrategy, "m")
	bar := func(index int64, rows map[string]Row, gap ...string) Frame {
		frame := frameOf(program, rows)
		frame.BarIndex = index
		if len(gap) > 0 {
			frame.FailedColumns = gapFailed("m", gap...)
		}
		return frame
	}
	first := evaluate(t, program, bar(0, map[string]Row{"X": values("m", 2), "Y": values("m", 1)}), State{})
	assertWeights(t, first, map[string]string{"X": "0.8"})

	// bar1：offset 1 本该重建，但批次含冻结的 X，延后；批次与建仓序号不变。
	second := evaluate(t, program, bar(1, map[string]Row{"X": {Values: map[string]float64{}}, "Y": values("m", 2), "Z": values("m", 1)}, "X"), first.State)
	assertOK(t, second)
	if !holdOf(second)["X"] {
		t.Fatalf("X 应保持：%v", second.Targets)
	}
	for i, batch := range second.State.Rules["h"].Batches {
		if batch.BaseWeights["X"] != "1" || batch.EstablishedBar != first.State.Rules["h"].Batches[i].EstablishedBar {
			t.Fatalf("延后期间批次不得改变：%+v", second.State.Rules["h"].Batches)
		}
	}

	// bar2：两个批次都到期，仍延后。
	third := evaluate(t, program, bar(2, map[string]Row{"X": {Values: map[string]float64{}}, "Y": values("m", 2), "Z": values("m", 1)}, "X"), second.State)
	if !holdOf(third)["X"] || third.State.Rules["h"].Batches[0].BaseWeights["X"] != "1" {
		t.Fatalf("连续延后：%+v %v", third.State.Rules["h"].Batches, third.Targets)
	}

	// bar3：空洞消失，到期批次补建；X 若不再是首选就被换掉，而不是在冻结期间被悄悄卖掉。
	fourth := evaluate(t, program, bar(3, map[string]Row{"X": values("m", 1), "Y": values("m", 2), "Z": values("m", 0.5)}), third.State)
	assertOK(t, fourth)
	if len(holdOf(fourth)) != 0 {
		t.Fatalf("空洞消失后不应再保持：%v", fourth.Targets)
	}
	assertWeights(t, fourth, map[string]string{"Y": "0.8"})
	for _, batch := range fourth.State.Rules["h"].Batches {
		if batch.BaseWeights["Y"] != "1" {
			t.Fatalf("到期批次应已补建为 Y：%+v", fourth.State.Rules["h"].Batches)
		}
	}
}
