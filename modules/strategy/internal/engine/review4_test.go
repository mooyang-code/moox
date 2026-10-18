package engine

import (
	"testing"
)

const catchUpDSL = `name: staged
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    holding: {bars: 3, offsets: [0]}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// 建仓 bar 被跳过（守门、无数据、停机）后，到期的批次不能在下一根 ok bar 上被丢弃清仓，而是按本期名次补建；
// 补建批次的建仓序号仍按 offset 对齐，下一次照常在 offset 上重建。
func TestHoldingCatchesUpSkippedRebuild(t *testing.T) {
	program := compile(t, catchUpDSL, "close")
	rows := map[string]Row{"A": values("close", 1), "B": values("close", 2)}
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}, Batches: []HoldingBatch{{Offset: 0, EstablishedBar: 3, BaseWeights: map[string]string{"A": "1"}}}}}}
	// bar 6 是建仓 bar，但没有产生 ok 决策；bar 7 上批次已满 3 根。
	frame := frameOf(program, rows)
	frame.BarIndex = 7
	decision := evaluate(t, program, frame, previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"B": "1"})
	batches := decision.State.Rules["r"].Batches
	if len(batches) != 1 || batches[0].Offset != 0 || batches[0].EstablishedBar != 6 || batches[0].BaseWeights["B"] != "1" {
		t.Fatalf("应在 bar 7 补建 offset 0 的批次，建仓序号对齐到 bar 6：%+v", batches)
	}
	if item := findItem(t, decision, "r", "B"); item.Stage != StageWeighted || item.Reason != "" {
		t.Fatalf("补建批次选中的标的应记为本期选中：%+v", item)
	}

	// bar 8 延续补建的批次，不再重建；bar 9 照常在 offset 上重建。
	rows["A"], rows["B"] = values("close", 3), values("close", 2)
	frame = frameOf(program, rows)
	frame.BarIndex = 8
	continued := evaluate(t, program, frame, decision.State)
	assertOK(t, continued)
	assertWeights(t, continued, map[string]string{"B": "1"})
	frame.BarIndex = 9
	rebuilt := evaluate(t, program, frame, continued.State)
	assertOK(t, rebuilt)
	assertWeights(t, rebuilt, map[string]string{"A": "1"})
	if batches := rebuilt.State.Rules["r"].Batches; len(batches) != 1 || batches[0].EstablishedBar != 9 {
		t.Fatalf("bar 9 应按 offset 重建：%+v", batches)
	}
}

// 补建同样要用本期分数选择：分数全部无效时整期 config_error，不能补建出一个空批次清仓。
func TestHoldingCatchUpRequiresValidScores(t *testing.T) {
	program := compile(t, `name: staged
rules:
  - id: r
    type: rank
    score: "1 / (close - open)"
    select: {top: 1}
    holding: {bars: 3, offsets: [0]}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "close", "open")
	rows := map[string]Row{"A": values("close", 10, "open", 10), "B": values("close", 5, "open", 5)}
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}, Batches: []HoldingBatch{{Offset: 0, EstablishedBar: 3, BaseWeights: map[string]string{"A": "1"}}}}}}
	frame := frameOf(program, rows)
	frame.BarIndex = 7
	assertSkipped(t, evaluate(t, program, frame, previous), SkipConfigError)
	// 还没到期的批次在非建仓 bar 上照常延续。
	frame.BarIndex = 5
	decision := evaluate(t, program, frame, previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "1"})
}
