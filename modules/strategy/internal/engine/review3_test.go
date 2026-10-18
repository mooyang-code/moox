package engine

import (
	"testing"
)

// signal 规则的持仓只判 exit：只缺 entry 用到的上一根（交叉入场）时照常判 exit，exit 为假就继续持有，
// 不能被当作缺数清仓；未持有的候选同时需要 entry 与 exit 的输入。
func TestSignalHeldInstrumentNeedsOnlyExitInputs(t *testing.T) {
	program := compile(t, `name: cross
rules:
  - id: trend
    type: signal
    pool: [A, B]
    entry: "bars[-1].bias <= 1 && bars[0].bias > 1"
    exit: "bars[0].bias < 1"
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "bias")
	rows := map[string]Row{
		"A": values("bias", 1.2),
		"B": values("bias", 1.5),
	}
	previous := State{Rules: map[string]RuleState{"trend": {Held: []string{"A"}}}}
	decision := evaluate(t, program, frameOf(program, rows), previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "1"})
	if item := findItem(t, decision, "trend", "B"); item.Stage != StageMissing || item.Reason != "missing:bars[-1].bias" {
		t.Fatalf("未持有的 B 缺上一根，应记为缺数：%+v", item)
	}

	rows["A"] = values("bias", 0.5)
	decision = evaluate(t, program, frameOf(program, rows), previous)
	assertOK(t, decision)
	if item := findItem(t, decision, "trend", "A"); item.Stage != StageDropped || item.Reason != "exit" {
		t.Fatalf("exit 为真时应退出：%+v", item)
	}
	delete(rows["A"].Values, "bias")
	decision = evaluate(t, program, frameOf(program, rows), previous)
	if item := findItem(t, decision, "trend", "A"); item.Stage != StageMissing || item.Reason != "missing:bias" {
		t.Fatalf("exit 的输入缺失时视为退出：%+v", item)
	}
}

// holding 的非建仓 bar 不使用本期分数：分数全部无效也照常延续批次，不整期跳过。
func TestHoldingContinuesWhenScoresInvalidOffRebuild(t *testing.T) {
	program := compile(t, `name: staged
rules:
  - id: r
    type: rank
    score: "1 / (close - open)"
    select: {top: 1}
    holding: {bars: 2, offsets: [0]}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "close", "open")
	rows := map[string]Row{"A": values("close", 10, "open", 10), "B": values("close", 5, "open", 5)}
	frame := frameOf(program, rows)
	frame.BarIndex = 1
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}, Batches: []HoldingBatch{{Offset: 0, EstablishedBar: 0, BaseWeights: map[string]string{"A": "1"}}}}}}
	decision := evaluate(t, program, frame, previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "1"})

	frame.BarIndex = 2
	assertSkipped(t, evaluate(t, program, frame, previous), SkipConfigError)
}

// View 级失败（K 线 View 声明的失败标的）记为 source_failed，不冒充因子失败。
func TestSourceFailureReason(t *testing.T) {
	program := compile(t, `name: plain
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "close")
	frame := frameOf(program, map[string]Row{"A": values("close", 1), "B": values("close", 2)})
	frame.FailedColumns = map[string]map[string]string{"close": {"B": FailedSource}}
	decision := evaluate(t, program, frame, State{})
	assertOK(t, decision)
	if item := findItem(t, decision, "r", "B"); item.Stage != StageMissing || item.Reason != "source_failed:close" {
		t.Fatalf("上游数据失败应记为 source_failed：%+v", item)
	}
}
