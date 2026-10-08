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

// evaluateRank 执行截面选股：filter → score → select（buffer 或 holding）→ weight → filter_after。
func evaluateRank(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState) (ruleResult, error) {
	result := ruleResult{weights: map[string]quant.Decimal{}}
	ruleID := rule.Rule.ID
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
				result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageFiltered, Reason: "filter"})
				continue
			}
		}
		passed = append(passed, id)
	}
	// score：截面函数在通过 filter 的样本上计算，结果无效的标的淘汰。
	scored := evaluateNumeric(rule.Score, passed, frame.Rows)
	for id, reason := range scored.failed {
		result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageDropped, Reason: reason})
	}
	live := without(passed, scored.failed)
	result.scored = len(live)
	bottom := rule.Rule.Select.Bottom > 0
	ordered := orderByRank(live, scored.values, bottom)
	rankOf := make(map[string]int, len(ordered))
	for i, id := range ordered {
		rankOf[id] = i + 1
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
				result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageScored, Score: formatScore(scored.values[id]), Rank: rankOf[id], Reason: "select.where"})
			}
		}
		ordered = kept
	}
	count := rule.Rule.Select.Top
	if bottom {
		count = rule.Rule.Select.Bottom
	}
	var selected []string
	var weights map[string]quant.Decimal
	if rule.Rule.Holding != nil {
		var err error
		selected, weights, result.state.Batches, err = evaluateHolding(rule, frame, sets, previous, ordered, scored.values, count)
		if err != nil {
			return ruleResult{}, err
		}
	} else {
		selected = selectWithBuffer(ordered, count, rule.Rule.Select.Buffer, previous.Held)
		weights = allocate(selected, rule.Rule.Weight)
		if rule.FilterAfter != nil {
			for _, id := range selected {
				ok, err := runBool(rule.FilterAfter, id, frame.Rows[id], scored.values[id])
				if err != nil {
					return ruleResult{}, fmt.Errorf("filter_after 在 %s 上执行失败：%w", id, err)
				}
				if !ok {
					delete(weights, id)
					result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageDropped, Score: formatScore(scored.values[id]), Rank: rankOf[id], Reason: "filter_after"})
				}
			}
		}
	}
	result.selected = len(selected)
	// 已打分但既未被选中也未进入权重（holding 延续的批次也算进入权重）的标的记为 not_selected。
	accounted := make(map[string]struct{}, len(selected)+len(weights))
	for _, id := range selected {
		accounted[id] = struct{}{}
	}
	for id := range weights {
		accounted[id] = struct{}{}
	}
	for _, id := range ordered {
		if _, ok := accounted[id]; ok {
			continue
		}
		result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageScored, Score: formatScore(scored.values[id]), Rank: rankOf[id], Reason: "not_selected"})
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
		item := Item{RuleID: ruleID, InstrumentID: id, Stage: StageWeighted, Weight: weight.String(), Rank: rankOf[id]}
		if value, ok := scored.values[id]; ok {
			item.Score = formatScore(value)
		}
		result.items = append(result.items, item)
	}
	sort.Strings(held)
	result.state.Held = held
	result.weights = weights
	return result, nil
}

// evaluateHolding 实现分批换仓：bar 序号落在某个 offset 上时用本期选择重建该批次，其余批次按基准权重延续；
// 缺数的持仓从批次移除，份额留作现金，不重新分配；满 bars 根的批次到期并在同一 bar 重建。
func evaluateHolding(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState, ordered []string, scores map[string]float64, count int) ([]string, map[string]quant.Decimal, []HoldingBatch, error) {
	holding := rule.Rule.Holding
	if frame.BarIndex < 0 {
		return nil, nil, nil, fmt.Errorf("holding 需要非负的 bar 序号，当前为 %d", frame.BarIndex)
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
	var selected []string
	if containsInt(holding.Offsets, offset) {
		selected = ordered
		if count < len(selected) {
			selected = selected[:count]
		}
		if rule.FilterAfter != nil {
			kept := make([]string, 0, len(selected))
			for _, id := range selected {
				ok, err := runBool(rule.FilterAfter, id, frame.Rows[id], scores[id])
				if err != nil {
					return nil, nil, nil, fmt.Errorf("filter_after 在 %s 上执行失败：%w", id, err)
				}
				if ok {
					kept = append(kept, id)
				}
			}
			selected = kept
		}
		base := baseShares(selected, quant.One(), rule.Rule.Weight.Method)
		raw := make(map[string]string, len(base))
		for id, weight := range base {
			raw[id] = weight.String()
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
				return nil, nil, nil, fmt.Errorf("批次 %d 中 %s 的基准权重无效：%w", batch.Offset, id, err)
			}
			weights[id] = weights[id].Add(base.Mul(budget))
		}
	}
	if rule.Rule.Weight.HasCap {
		weights = applyCap(sortedIDs(weights), weights, rule.Rule.Weight.Cap)
	}
	return selected, weights, batches, nil
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
