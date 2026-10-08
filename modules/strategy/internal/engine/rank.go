package engine

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// ruleResult 是一条规则的求值结果。
type ruleResult struct {
	weights  map[string]quant.Decimal
	state    RuleState
	items    []Item
	filtered int
	scored   int
	selected int
}

// 明细原因：holding 规则下只由延续批次持有的标的记 holding，本期又在某个阶段被淘汰时记 holding:<原因>；
// 非建仓 bar 上未持有的标的记 not_rebalanced。
const (
	reasonHolding       = "holding"
	reasonNotSelected   = "not_selected"
	reasonNotRebalanced = "not_rebalanced"
	reasonNotExpected   = "not_expected"
)

// explanation 收集一条规则的解释明细，保证每个标的只有一条。
type explanation struct {
	ruleID string
	items  map[string]Item
}

func newExplanation(ruleID string) *explanation {
	return &explanation{ruleID: ruleID, items: make(map[string]Item)}
}

// reject 记录本期在某个阶段被淘汰的标的。
func (e *explanation) reject(id, stage, reason string, score *float64, rank int) {
	item := Item{RuleID: e.ruleID, InstrumentID: id, Stage: stage, Reason: reason, Rank: rank}
	if score != nil {
		item.Score = formatScore(*score)
	}
	e.items[id] = item
}

// weight 记录进入目标权重的标的。fresh 为 false 表示权重只来自延续批次；
// 该标的本期若已被淘汰，最终阶段仍是 weighted，原因保留本期的淘汰原因。
func (e *explanation) weight(id string, weight quant.Decimal, score *float64, rank int, fresh bool) {
	item := Item{RuleID: e.ruleID, InstrumentID: id, Stage: StageWeighted, Weight: weight.String(), Rank: rank}
	if score != nil {
		item.Score = formatScore(*score)
	}
	if !fresh {
		item.Reason = reasonHolding
		if previous, ok := e.items[id]; ok && previous.Reason != "" && previous.Reason != reasonNotRebalanced {
			item.Reason = reasonHolding + ":" + previous.Reason
		}
	}
	e.items[id] = item
}

func (e *explanation) list() []Item {
	items := make([]Item, 0, len(e.items))
	for _, item := range e.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].InstrumentID < items[j].InstrumentID })
	return items
}

// evaluateRank 执行截面选股：filter → score → select（buffer 或 holding）→ weight → filter_after。
func evaluateRank(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState) (ruleResult, error) {
	result := ruleResult{weights: map[string]quant.Decimal{}}
	explain := newExplanation(rule.Rule.ID)
	// filter：引用列已由集合划分保证存在。
	passed := make([]string, 0, len(sets.available))
	for _, id := range sets.available {
		if rule.Filter != nil {
			ok, err := runBool(rule.Filter, id, frame.Rows[id], 0)
			if err != nil {
				return ruleResult{}, fmt.Errorf("filter 在 %s 上执行失败：%w", id, err)
			}
			if !ok {
				result.filtered++
				explain.reject(id, StageFiltered, "filter", nil, 0)
				continue
			}
		}
		passed = append(passed, id)
	}
	// score：截面函数在通过 filter 的样本上计算，结果无效的标的淘汰。
	scored := evaluateNumeric(rule.Score, passed, frame.Rows)
	for id, reason := range scored.failed {
		explain.reject(id, StageDropped, reason, nil, 0)
	}
	live := without(passed, scored.failed)
	if len(passed) > 0 && len(live) == 0 {
		return ruleResult{}, fmt.Errorf("score 对全部 %d 个通过 filter 的候选都无效", len(passed))
	}
	result.scored = len(live)
	bottom := rule.Rule.Select.Bottom > 0
	ordered := orderByRank(live, scored.values, bottom)
	rankOf := make(map[string]int, len(ordered))
	for i, id := range ordered {
		rankOf[id] = i + 1
	}
	scoreOf := func(id string) *float64 {
		if value, ok := scored.values[id]; ok {
			return &value
		}
		return nil
	}
	// select.where：带 score 的二次筛选，名次仍按全样本计算。
	if rule.SelectWhere != nil {
		kept := make([]string, 0, len(ordered))
		for _, id := range ordered {
			ok, err := runBool(rule.SelectWhere, id, frame.Rows[id], scored.values[id])
			if err != nil {
				return ruleResult{}, fmt.Errorf("select.where 在 %s 上执行失败：%w", id, err)
			}
			if ok {
				kept = append(kept, id)
			} else {
				explain.reject(id, StageScored, "select.where", scoreOf(id), rankOf[id])
			}
		}
		ordered = kept
	}
	count := rule.Rule.Select.Top
	if bottom {
		count = rule.Rule.Select.Bottom
	}
	// chosen 是本期按名次选中的标的（filter_after 之前）；fresh 是本期选择最终给出权重的标的。
	var chosen []string
	var weights map[string]quant.Decimal
	fresh := make(map[string]struct{})
	notChosenReason := reasonNotSelected
	if rule.Rule.Holding != nil {
		outcome, err := evaluateHolding(rule, frame, sets, previous, ordered, scored.values, count, explain, rankOf)
		if err != nil {
			return ruleResult{}, err
		}
		chosen, weights, result.state.Batches, fresh = outcome.chosen, outcome.weights, outcome.batches, outcome.fresh
		if !outcome.rebuilt {
			notChosenReason = reasonNotRebalanced
		}
	} else {
		chosen = selectWithBuffer(ordered, rankOf, count, rule.Rule.Select.Buffer, previous.Held)
		weights = allocate(chosen, rule.Rule.Weight)
		for _, id := range chosen {
			if rule.FilterAfter != nil {
				ok, err := runBool(rule.FilterAfter, id, frame.Rows[id], scored.values[id])
				if err != nil {
					return ruleResult{}, fmt.Errorf("filter_after 在 %s 上执行失败：%w", id, err)
				}
				if !ok {
					// 只剔除不补位：剔除的份额留现金。
					delete(weights, id)
					explain.reject(id, StageDropped, "filter_after", scoreOf(id), rankOf[id])
					continue
				}
			}
			fresh[id] = struct{}{}
		}
	}
	result.selected = len(chosen)
	chosenSet := make(map[string]struct{}, len(chosen))
	for _, id := range chosen {
		chosenSet[id] = struct{}{}
	}
	for _, id := range ordered {
		if _, ok := chosenSet[id]; !ok {
			explain.reject(id, StageScored, notChosenReason, scoreOf(id), rankOf[id])
		}
	}
	short := rule.Rule.Side == dsl.SideShort
	held := make([]string, 0, len(weights))
	for _, id := range sortedIDs(weights) {
		weight := weights[id]
		if weight.IsZero() {
			delete(weights, id)
			continue
		}
		if short {
			weight = weight.Neg()
			weights[id] = weight
		}
		held = append(held, id)
		_, isFresh := fresh[id]
		explain.weight(id, weight, scoreOf(id), rankOf[id], isFresh)
	}
	result.state.Held = held
	result.weights = weights
	result.items = explain.list()
	return result, nil
}

// holdingOutcome 是分批换仓一期的结果。
type holdingOutcome struct {
	rebuilt bool
	chosen  []string
	fresh   map[string]struct{}
	weights map[string]quant.Decimal
	batches []HoldingBatch
}

// evaluateHolding 实现分批换仓：bar 序号落在某个 offset 上时用本期选择重建该批次，其余批次按基准权重延续。
// 基准份额在 filter_after 之前按选中数量计算，filter_after 剔除与缺数移除的份额都留作现金，不重新分配；
// 满 bars 根的批次到期并在同一 bar 重建。
func evaluateHolding(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState, ordered []string, scores map[string]float64, count int, explain *explanation, rankOf map[string]int) (holdingOutcome, error) {
	holding := rule.Rule.Holding
	outcome := holdingOutcome{fresh: map[string]struct{}{}}
	if frame.BarIndex < 0 {
		return outcome, fmt.Errorf("holding 需要非负的 bar 序号，当前为 %d", frame.BarIndex)
	}
	availableSet := make(map[string]struct{}, len(sets.available))
	for _, id := range sets.available {
		availableSet[id] = struct{}{}
	}
	batches := make([]HoldingBatch, 0, len(previous.Batches)+1)
	for _, batch := range previous.Batches {
		if frame.BarIndex-batch.EstablishedBar >= int64(holding.Bars) {
			continue
		}
		kept := make(map[string]string, len(batch.BaseWeights))
		for id, raw := range batch.BaseWeights {
			if _, ok := availableSet[id]; ok {
				kept[id] = raw
			}
		}
		batches = append(batches, HoldingBatch{Offset: batch.Offset, EstablishedBar: batch.EstablishedBar, BaseWeights: kept})
	}
	offset := int(frame.BarIndex % int64(holding.Bars))
	if containsInt(holding.Offsets, offset) {
		outcome.rebuilt = true
		chosen := ordered
		if count < len(chosen) {
			chosen = chosen[:count]
		}
		outcome.chosen = chosen
		base := baseShares(chosen, quant.One(), rule.Rule.Weight.Method)
		raw := make(map[string]string, len(base))
		for _, id := range chosen {
			if rule.FilterAfter != nil {
				score := scores[id]
				ok, err := runBool(rule.FilterAfter, id, frame.Rows[id], score)
				if err != nil {
					return outcome, fmt.Errorf("filter_after 在 %s 上执行失败：%w", id, err)
				}
				if !ok {
					explain.reject(id, StageDropped, "filter_after", &score, rankOf[id])
					continue
				}
			}
			raw[id] = base[id].String()
			outcome.fresh[id] = struct{}{}
		}
		rebuilt := HoldingBatch{Offset: offset, EstablishedBar: frame.BarIndex, BaseWeights: raw}
		replaced := false
		for i := range batches {
			if batches[i].Offset == offset {
				batches[i] = rebuilt
				replaced = true
			}
		}
		if !replaced {
			batches = append(batches, rebuilt)
		}
	}
	sort.Slice(batches, func(i, j int) bool { return batches[i].Offset < batches[j].Offset })
	sortedOffsets := append([]int(nil), holding.Offsets...)
	sort.Ints(sortedOffsets)
	offsetKeys := make([]string, 0, len(sortedOffsets))
	for _, value := range sortedOffsets {
		offsetKeys = append(offsetKeys, fmt.Sprint(value))
	}
	perBatch := quant.DivideStable(rule.Rule.Weight.Total, offsetKeys)
	weights := make(map[string]quant.Decimal)
	for _, batch := range batches {
		budget := perBatch[fmt.Sprint(batch.Offset)]
		for id, raw := range batch.BaseWeights {
			base, err := quant.Parse(raw)
			if err != nil {
				return outcome, fmt.Errorf("批次 %d 中 %s 的基准权重无效：%w", batch.Offset, id, err)
			}
			weights[id] = weights[id].Add(base.Mul(budget))
		}
	}
	if rule.Rule.Weight.HasCap {
		weights = applyCap(sortedIDs(weights), weights, rule.Rule.Weight.Cap)
	}
	outcome.weights = weights
	outcome.batches = batches
	return outcome, nil
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
