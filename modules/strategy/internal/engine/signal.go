package engine

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// evaluateSignal 执行固定池择时：先对旧理论持仓判 exit，再对候选判 entry，退出优先；
// 持有标的缺数视为退出；本期持有的标的平分规则预算。
func evaluateSignal(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState) (ruleResult, error) {
	result := ruleResult{weights: map[string]quant.Decimal{}}
	ruleID := rule.Rule.ID
	availableSet := make(map[string]struct{}, len(sets.available))
	for _, id := range sets.available {
		availableSet[id] = struct{}{}
	}
	heldBefore := make(map[string]struct{}, len(previous.Held))
	held := make([]string, 0, len(previous.Held)+len(sets.available))
	for _, id := range previous.Held {
		heldBefore[id] = struct{}{}
		if _, ok := availableSet[id]; !ok {
			// 缺数的持仓按清仓处理；明细已由集合划分记录为 missing / aged_out。
			continue
		}
		exit, err := runBool(rule.Exit, id, frame.Rows[id], 0)
		if err != nil {
			return ruleResult{}, fmt.Errorf("exit 在 %s 上执行失败：%w", id, err)
		}
		if exit {
			result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageDropped, Reason: "exit"})
			continue
		}
		held = append(held, id)
	}
	for _, id := range sets.available {
		if _, was := heldBefore[id]; was {
			continue
		}
		entry, err := runBool(rule.Entry, id, frame.Rows[id], 0)
		if err != nil {
			return ruleResult{}, fmt.Errorf("entry 在 %s 上执行失败：%w", id, err)
		}
		if !entry {
			result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageIdle, Reason: "no_entry"})
			continue
		}
		exit, err := runBool(rule.Exit, id, frame.Rows[id], 0)
		if err != nil {
			return ruleResult{}, fmt.Errorf("exit 在 %s 上执行失败：%w", id, err)
		}
		if exit {
			result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageIdle, Reason: "entry_and_exit"})
			continue
		}
		held = append(held, id)
	}
	sort.Strings(held)
	result.scored = len(sets.available)
	result.selected = len(held)
	weights := quant.DivideStable(rule.Rule.Weight.Total, held)
	short := rule.Rule.Side == dsl.SideShort
	for _, id := range held {
		weight := weights[id]
		if short {
			weight = weight.Neg()
		}
		weights[id] = weight
		result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageWeighted, Weight: weight.String()})
	}
	result.weights = weights
	result.state.Held = held
	return result, nil
}
