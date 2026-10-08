package engine

import (
	"strings"
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

	// 0.1 × 3 的均值有浮点误差，也必须判为零方差；只差一个 ulp 的样本同样记 0。
	for name, rows := range map[string]map[string]Row{
		"相同的 0.1": {"A": values("x", 0.1), "B": values("x", 0.1), "C": values("x", 0.1)},
		"差一个 ulp": {"A": values("x", 0.1), "B": values("x", 0.1), "C": values("x", 0.30000000000000004/3)},
	} {
		decision := evaluate(t, zProgram, frameOf(zProgram, rows), State{})
		for _, id := range []string{"A", "B", "C"} {
			if item := findItem(t, decision, "r", id); item.Score != "0" {
				t.Fatalf("%s：零方差 zscore 应为 0：%+v", name, item)
			}
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

// 截面函数的样本与书写顺序无关：任一内层表达式无效的标的不参与任何一个截面函数。
func TestNormalizerSampleIndependentOfOrder(t *testing.T) {
	strategy := `name: order
rules:
  - id: r
    type: rank
    score: "SCORE"
    select: {top: 3}
    weight: {total: 1}
portfolio:
  max_missing: 1
`
	rows := map[string]Row{
		"A": values("a", 1, "b", 3, "d", 0),
		"B": values("a", 1, "b", 1, "d", 1),
		"C": values("a", 2, "b", 2, "d", 1),
	}
	var fingerprints []string
	for _, score := range []string{"rank(a / d) + rank(b)", "rank(b) + rank(a / d)"} {
		program := compile(t, strings.Replace(strategy, "SCORE", score, 1), "a", "b", "d")
		decision := evaluate(t, program, frameOf(program, rows), State{})
		assertOK(t, decision)
		if item := findItem(t, decision, "r", "A"); item.Stage != StageDropped || item.Reason != "score_invalid" {
			t.Fatalf("%s：A 的分数应无效：%+v", score, item)
		}
		for id, want := range map[string]string{"B": "0", "C": "2"} {
			if item := findItem(t, decision, "r", id); item.Score != want {
				t.Fatalf("%s：%s 的分数应为 %s：%+v", score, id, want, item)
			}
		}
		fingerprints = append(fingerprints, fingerprint(t, decision))
	}
	if fingerprints[0] != fingerprints[1] {
		t.Fatalf("交换截面函数顺序后结果不同：%s 与 %s", fingerprints[0], fingerprints[1])
	}
}

// 延续批次中的标的本期被 filter 淘汰时仍按批次持有，明细只有一条 weighted，原因 holding:filter；
// 非建仓 bar 上未持有的标的记 not_rebalanced。
func TestHoldingContinuingBatchSurvivesFilterWithSingleItem(t *testing.T) {
	program := compile(t, `name: hold_filter
rules:
  - id: h
    type: rank
    filter: "m > 0"
    score: "m"
    select: {top: 1}
    weight: {total: 0.8}
    holding: {bars: 2, offsets: [0]}
portfolio:
  max_missing: 1
`, "m")
	bar0 := frameOf(program, map[string]Row{"A": values("m", 2), "B": values("m", 1)})
	bar0.BarIndex = 0
	first := evaluate(t, program, bar0, State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"A": "0.8"})
	if item := findItem(t, first, "h", "A"); item.Stage != StageWeighted || item.Reason != "" {
		t.Fatalf("建仓 bar 上 A 是本期选中：%+v", item)
	}

	bar1 := frameOf(program, map[string]Row{"A": values("m", -1), "B": values("m", 1)})
	bar1.BarIndex = 1
	second := evaluate(t, program, bar1, first.State)
	assertOK(t, second)
	assertWeights(t, second, map[string]string{"A": "0.8"})
	if item := findItem(t, second, "h", "A"); item.Stage != StageWeighted || item.Reason != "holding:filter" || item.Weight != "0.8" {
		t.Fatalf("A 应由延续批次持有并注明本期被 filter 淘汰：%+v", item)
	}
	if item := findItem(t, second, "h", "B"); item.Stage != StageScored || item.Reason != "not_rebalanced" {
		t.Fatalf("非建仓 bar 上 B 应为 not_rebalanced：%+v", item)
	}
	if summary := second.Summary.Rules["h"]; summary.Filtered != 1 || summary.Selected != 0 || summary.Weighted != 1 {
		t.Fatalf("摘要不符：%+v", summary)
	}
}

// holding 建仓时 filter_after 剔除的份额留现金，不分给同批次的其他标的；延续期间份额不变。
func TestHoldingFilterAfterLeavesShareAsCash(t *testing.T) {
	program := compile(t, `name: hold_after
rules:
  - id: h
    type: rank
    score: "m"
    select: {top: 2}
    filter_after: "ok > 0"
    weight: {total: 0.8}
    holding: {bars: 2, offsets: [0]}
portfolio:
  max_missing: 1
`, "m", "ok")
	rows := map[string]Row{"A": values("m", 3, "ok", 0), "B": values("m", 2, "ok", 1), "C": values("m", 1, "ok", 1)}
	bar0 := frameOf(program, rows)
	bar0.BarIndex = 0
	first := evaluate(t, program, bar0, State{})
	assertOK(t, first)
	assertWeights(t, first, map[string]string{"B": "0.4"})
	assertDecimal(t, "cash", first.Summary.Cash, "0.6")
	if item := findItem(t, first, "h", "A"); item.Stage != StageDropped || item.Reason != "filter_after" || item.Rank != 1 {
		t.Fatalf("A 应被 filter_after 剔除：%+v", item)
	}
	if item := findItem(t, first, "h", "C"); item.Stage != StageScored || item.Reason != "not_selected" {
		t.Fatalf("C 应为未入选：%+v", item)
	}
	if batches := first.State.Rules["h"].Batches; len(batches) != 1 || len(batches[0].BaseWeights) != 1 || batches[0].BaseWeights["B"] != "0.5" {
		t.Fatalf("批次应只含 B 且基准份额为 0.5：%+v", batches)
	}

	bar1 := frameOf(program, rows)
	bar1.BarIndex = 1
	second := evaluate(t, program, bar1, first.State)
	assertOK(t, second)
	assertWeights(t, second, map[string]string{"B": "0.4"})
	if item := findItem(t, second, "h", "B"); item.Stage != StageWeighted || item.Reason != "holding" {
		t.Fatalf("B 应由延续批次持有：%+v", item)
	}
}

// 外层结果无效（除以名次 0）只淘汰该标的本身，不触发截面函数重算，不会一轮淘汰一个直到只剩一个。
func TestOuterInvalidScoreDoesNotCascade(t *testing.T) {
	program := compile(t, `name: ratio
rules:
  - id: r
    type: rank
    score: "rank(a) / rank(b)"
    select: {top: 5}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "a", "b")
	rows := map[string]Row{}
	for i, id := range numberedIDs("X", 20) {
		rows[id] = values("a", i, "b", 20-i)
	}
	decision := evaluate(t, program, frameOf(program, rows), State{})
	assertOK(t, decision)
	if summary := decision.Summary.Rules["r"]; summary.Scored != 19 || summary.Selected != 5 {
		t.Fatalf("只有 rank(b)=0 的一个标的无效：%+v", summary)
	}
	invalid := 0
	for _, item := range decision.Items {
		if item.Reason == "score_invalid" {
			invalid++
		}
	}
	if invalid != 1 {
		t.Fatalf("应只有 1 条 score_invalid，实际 %d", invalid)
	}
}

// 通过 filter 的候选全部分数无效时整期 config_error，不能当作"选不出标的"清仓。
func TestAllScoresInvalidSkips(t *testing.T) {
	program := compile(t, `name: broken
rules:
  - id: r
    type: rank
    score: "x / y"
    select: {top: 1}
    weight: {total: 1}
`, "x", "y")
	previous := State{Rules: map[string]RuleState{"r": {Held: []string{"A"}}}, Targets: map[string]string{"A": "1"}}
	decision := evaluate(t, program, frameOf(program, map[string]Row{"A": values("x", 1, "y", 0), "B": values("x", 2, "y", 0)}), previous)
	assertSkipped(t, decision, SkipConfigError)
	if decision.State.Targets["A"] != "1" {
		t.Fatalf("跳过时应沿用前序：%+v", decision.State)
	}
}

// buffer 按全样本名次（与解释明细一致）判断"前 N+B 名"：select.where 剔除第 1 名后，名次 3 的上期持仓不在前 2 名。
func TestBufferUsesFullSampleRanks(t *testing.T) {
	program := compile(t, `name: buffered
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1, buffer: 1, where: "score < 3"}
    weight: {total: 1}
portfolio:
  max_missing: 1
`, "m")
	frame := frameOf(program, map[string]Row{"A": values("m", 3), "B": values("m", 2), "C": values("m", 1)})
	decision := evaluate(t, program, frame, State{Rules: map[string]RuleState{"r": {Held: []string{"C"}}}})
	assertOK(t, decision)
	assertWeights(t, decision, map[string]string{"B": "1"})
	if item := findItem(t, decision, "r", "C"); item.Rank != 3 || item.Reason != "not_selected" {
		t.Fatalf("C 的全样本名次为 3，超出 buffer：%+v", item)
	}
}
