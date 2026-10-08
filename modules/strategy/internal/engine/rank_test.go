package engine

import (
	"testing"
)

const bufferStrategy = `name: buffer
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2, buffer: 1}
    weight: {total: 0.8}
portfolio:
  max_missing: 1
`

// S3：上期持有、本期缺数的标的目标权重为 0，解释写 missing:<col>，buffer 不保留它。
func TestS3HeldInstrumentMissingIsDroppedAndNotBuffered(t *testing.T) {
	program := compile(t, bufferStrategy, "m")
	first := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 3), "B": values("m", 2), "C": values("m", 1), "D": values("m", 0.5),
	}), State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"A": "0.4", "B": "0.4"})
	if held := first.State.Rules["r"].Held; len(held) != 2 || held[0] != "A" || held[1] != "B" {
		t.Fatalf("规则状态应记录持有 A、B：%v", held)
	}
	second := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("volume", 1), "B": values("m", 2), "C": values("m", 3), "D": values("m", 1),
	}), first.State)
	assertOK(t, second)
	assertWeights(t, second, map[string]string{"B": "0.4", "C": "0.4"})
	if item := findItem(t, second, "r", "A"); item.Stage != StageMissing || item.Reason != "missing:m" {
		t.Fatalf("A 应记为缺数：%+v", item)
	}
	if second.Summary.Rules["r"].Missing != 1 {
		t.Fatalf("缺数应为 1：%+v", second.Summary.Rules["r"])
	}
}

// buffer：上期持有且仍在前 top+buffer 名的标的保留，即使有更高分的新标的。
func TestBufferKeepsHeldWithinRange(t *testing.T) {
	program := compile(t, bufferStrategy, "m")
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A", "B"}}}}
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 1), "B": values("m", 2), "C": values("m", 3), "D": values("m", 0.5),
	}), previous)
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.4", "B": "0.4"})
	if item := findItem(t, decision, "r", "C"); item.Stage != StageScored || item.Rank != 1 || item.Reason != "not_selected" {
		t.Fatalf("C 应为第 1 名但未入选：%+v", item)
	}
	// 跌出缓冲区的持仓被替换。
	dropped := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 0.1), "B": values("m", 2), "C": values("m", 3), "D": values("m", 1),
	}), previous)
	assertWeights(t, dropped, map[string]string{"B": "0.4", "C": "0.4"})
}

// S4：top=5、total=0.8、cap=0.3，只有 2 个标的通过：分配 0.6，其余留现金。
func TestS4CapLeavesRemainderAsCash(t *testing.T) {
	program := compile(t, `name: s4
rules:
  - id: r
    type: rank
    filter: "pass > 0"
    score: "m"
    select: {top: 5}
    weight: {total: 0.8, cap: 0.3}
`, "pass", "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("pass", 1, "m", 1), "B": values("pass", 1, "m", 2), "C": values("pass", 0, "m", 3),
	}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.3", "B": "0.3"})
	assertDecimal(t, "cash", decision.Summary.Cash, "0.4")
	assertDecimal(t, "allocated", decision.Summary.Rules["r"].Allocated, "0.6")
	if decision.Summary.Rules["r"].Filtered != 1 || findItem(t, decision, "r", "C").Stage != StageFiltered {
		t.Fatalf("C 应被 filter 淘汰：%+v", decision.Summary.Rules["r"])
	}
}

// cap 的超出部分在规则内按比例分给未达上限的标的。
func TestCapRedistributesWithinRule(t *testing.T) {
	program := compile(t, `name: cap
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 3}
    weight: {total: 0.9, method: rank, cap: 0.4}
`, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 3), "B": values("m", 2), "C": values("m", 1),
	}), State{})
	assertOK(t, decision)
	// rank 配权：A 0.45、B 0.3、C 0.15；A 超出 0.05 按 0.3:0.15 分给 B、C。
	assertWeights(t, decision, map[string]string{"A": "0.4", "B": "0.333333333333333333", "C": "0.166666666666666666"})
	assertDecimal(t, "allocated", decision.Summary.Rules["r"].Allocated, "0.899999999999999999")
}

// S6：bottom 选择配合 method=rank，低分标的得到更高权重。
func TestS6BottomWithRankWeights(t *testing.T) {
	program := compile(t, `name: s6
rules:
  - id: r
    type: rank
    score: "m"
    select: {bottom: 3}
    weight: {total: 0.6, method: rank}
`, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 1), "B": values("m", 2), "C": values("m", 3), "D": values("m", 4),
	}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.3", "B": "0.2", "C": "0.1"})
	if item := findItem(t, decision, "r", "D"); item.Stage != StageScored || item.Rank != 4 {
		t.Fatalf("D 在 bottom 排序下应为第 4 名：%+v", item)
	}
}

// S7：rank 同分取平均名次、单样本 0.5、zscore 零方差为 0、rank(zscore(x)) 嵌套，结果确定。
func TestS7NormalizersAreDeterministic(t *testing.T) {
	rankProgram := compile(t, `name: s7
rules:
  - id: r
    type: rank
    score: "rank(x)"
    select: {top: 4}
    weight: {total: 1}
`, "x")
	ties := evaluate(t, rankProgram, frameOf(rankProgram, map[string]Row{
		"A": values("x", 1), "B": values("x", 2), "C": values("x", 2), "D": values("x", 3),
	}), State{})
	assertOK(t, ties)
	for id, want := range map[string]string{"A": "0", "B": "0.5", "C": "0.5", "D": "1"} {
		if item := findItem(t, ties, "r", id); item.Score != want {
			t.Fatalf("%s 的 rank 应为 %s：%+v", id, want, item)
		}
	}
	single := evaluate(t, rankProgram, frameOf(rankProgram, map[string]Row{"A": values("x", 7)}), State{})
	if item := findItem(t, single, "r", "A"); item.Score != "0.5" {
		t.Fatalf("单样本 rank 应为 0.5：%+v", item)
	}

	zProgram := compile(t, `name: s7z
rules:
  - id: r
    type: rank
    score: "zscore(x)"
    select: {top: 4}
    weight: {total: 1}
`, "x")
	flat := evaluate(t, zProgram, frameOf(zProgram, map[string]Row{
		"A": values("x", 5), "B": values("x", 5), "C": values("x", 5),
	}), State{})
	assertOK(t, flat)
	for _, id := range []string{"A", "B", "C"} {
		if item := findItem(t, flat, "r", id); item.Score != "0" {
			t.Fatalf("零方差 zscore 应为 0：%+v", item)
		}
	}

	nested := compile(t, `name: s7n
rules:
  - id: r
    type: rank
    score: "rank(zscore(x))"
    select: {top: 4}
    weight: {total: 1}
`, "x")
	frame := frameOf(nested, map[string]Row{"A": values("x", 1), "B": values("x", 2), "C": values("x", 3)})
	first := evaluate(t, nested, frame, State{})
	assertOK(t, first)
	for id, want := range map[string]string{"A": "0", "B": "0.5", "C": "1"} {
		if item := findItem(t, first, "r", id); item.Score != want {
			t.Fatalf("%s 的 rank(zscore) 应为 %s：%+v", id, want, item)
		}
	}
	second := evaluate(t, nested, frame, State{})
	if fingerprint(t, first) != fingerprint(t, second) {
		t.Fatal("嵌套截面函数两次求值结果不同")
	}
}

// S8：选择结果为空仍是 ok，零目标、现金 100%。
func TestS8EmptySelectionIsOKWithFullCash(t *testing.T) {
	program := compile(t, `name: s8
rules:
  - id: r
    type: rank
    filter: "m > 100"
    score: "m"
    select: {top: 2}
    weight: {total: 0.8}
`, "m")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 1), "B": values("m", 2), "C": values("m", 3),
	}), State{})
	assertOK(t, decision)
	if len(decision.Targets) != 0 {
		t.Fatalf("不应有目标：%v", decision.Targets)
	}
	assertDecimal(t, "cash", decision.Summary.Cash, "1")
	assertDecimal(t, "gross", decision.Summary.Gross, "0")
	if summary := decision.Summary.Rules["r"]; summary.Filtered != 3 || summary.Selected != 0 {
		t.Fatalf("摘要不符：%+v", summary)
	}
	if len(decision.State.Rules["r"].Held) != 0 {
		t.Fatalf("规则状态不应持有：%v", decision.State.Rules["r"])
	}
}

// select.where 用 score 二次筛选，filter_after 只剔除不补位。
func TestSelectWhereAndFilterAfter(t *testing.T) {
	program := compile(t, `name: where
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 3, where: "score > 1"}
    filter_after: "ok > 0"
    weight: {total: 0.9}
`, "m", "ok")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 3, "ok", 1), "B": values("m", 2, "ok", 0), "C": values("m", 1, "ok", 1), "D": values("m", 0.5, "ok", 1),
	}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"A": "0.45"})
	assertDecimal(t, "cash", decision.Summary.Cash, "0.55")
	if item := findItem(t, decision, "r", "B"); item.Stage != StageDropped || item.Reason != "filter_after" || item.Rank != 2 {
		t.Fatalf("B 应被 filter_after 剔除：%+v", item)
	}
	if item := findItem(t, decision, "r", "C"); item.Stage != StageScored || item.Reason != "select.where" || item.Rank != 3 {
		t.Fatalf("C 应被 select.where 淘汰：%+v", item)
	}
	if summary := decision.Summary.Rules["r"]; summary.Selected != 2 || summary.Weighted != 1 || summary.Scored != 4 {
		t.Fatalf("摘要不符：%+v", summary)
	}
}

// 分数无效（NaN / 除零）的标的被淘汰而不是整期失败。
func TestInvalidScoreDropsInstrument(t *testing.T) {
	program := compile(t, `name: nan
rules:
  - id: r
    type: rank
    score: "m / d"
    select: {top: 3}
    weight: {total: 1}
`, "m", "d")
	decision := evaluate(t, program, frameOf(program, map[string]Row{
		"A": values("m", 1, "d", 0), "B": values("m", 2, "d", 1), "C": values("m", 3, "d", 1),
	}), State{})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"B": "0.5", "C": "0.5"})
	if item := findItem(t, decision, "r", "A"); item.Stage != StageDropped || item.Reason != "score_invalid" {
		t.Fatalf("A 的分数应无效：%+v", item)
	}
}

// holding：按 offset 分批建仓，缺数持仓从批次移除且份额留现金，到期批次在同一 bar 重建。
func TestHoldingBatchesAndMissingRemoval(t *testing.T) {
	program := compile(t, `name: hold
rules:
  - id: h
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 0.8}
    holding: {bars: 2, offsets: [0, 1]}
portfolio:
  max_missing: 1
`, "m")
	bar0 := frameOf(program, map[string]Row{"X": values("m", 2), "Y": values("m", 1)})
	bar0.BarIndex = 0
	first := evaluate(t, program, bar0, State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"X": "0.4"})
	batches := first.State.Rules["h"].Batches
	if len(batches) != 1 || batches[0].Offset != 0 || batches[0].EstablishedBar != 0 || batches[0].BaseWeights["X"] != "1" {
		t.Fatalf("批次不符：%+v", batches)
	}

	bar1 := frameOf(program, map[string]Row{"Y": values("m", 2), "Z": values("m", 1)})
	bar1.BarIndex = 1
	bar1.Expected["h"] = []string{"X", "Y", "Z"}
	second := evaluate(t, program, bar1, first.State)
	assertOK(t, second)
	assertWeights(t, second, map[string]string{"Y": "0.4"})
	assertDecimal(t, "cash", second.Summary.Cash, "0.6")
	if item := findItem(t, second, "h", "X"); item.Stage != StageMissing || item.Reason != "no_row" {
		t.Fatalf("X 应记为缺数：%+v", item)
	}
	batches = second.State.Rules["h"].Batches
	if len(batches) != 2 || len(batches[0].BaseWeights) != 0 || batches[1].Offset != 1 || batches[1].BaseWeights["Y"] != "1" {
		t.Fatalf("批次不符：%+v", batches)
	}

	bar2 := frameOf(program, map[string]Row{"X": values("m", 3), "Y": values("m", 2), "Z": values("m", 1)})
	bar2.BarIndex = 2
	third := evaluate(t, program, bar2, second.State)
	assertOK(t, third)
	assertWeights(t, third, map[string]string{"X": "0.4", "Y": "0.4"})
	batches = third.State.Rules["h"].Batches
	if len(batches) != 2 || batches[0].EstablishedBar != 2 || batches[0].BaseWeights["X"] != "1" || batches[1].EstablishedBar != 1 {
		t.Fatalf("到期批次应重建：%+v", batches)
	}
	if item := findItem(t, third, "h", "Y"); item.Stage != StageWeighted {
		t.Fatalf("延续批次中的 Y 应进入权重：%+v", item)
	}
	if item := findItem(t, third, "h", "Z"); item.Stage != StageScored || item.Reason != "not_selected" {
		t.Fatalf("Z 应为未入选：%+v", item)
	}
}

// min_universe：进入 filter 的可用标的不足即整期跳过，状态沿用前序。
func TestMinUniverseGuard(t *testing.T) {
	program := compile(t, `name: guard
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  min_universe: 3
`, "m")
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}}}, Targets: map[string]string{"A": "1"}}
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("m", 1), "B": values("m", 2)}), previous)
	assertSkipped(t, decision, SkipUniverseTooSmall)
	if decision.State.Rules["r"].Held[0] != "A" || decision.State.Targets["A"] != "1" {
		t.Fatalf("跳过时状态应沿用前序：%+v", decision.State)
	}
	if decision.Summary.Rules["r"].Available != 2 {
		t.Fatalf("跳过时也应给出集合摘要：%+v", decision.Summary.Rules["r"])
	}
}
