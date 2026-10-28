package engine

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// evaluateSignal 执行固定池择时：先对旧理论持仓判 exit，再对候选判 entry，退出优先；
// 持有标的的 exit 输入缺失视为退出；本期持有的标的平分规则预算。
func evaluateSignal(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState, tolerance quant.Decimal) (ruleResult, error) {
	result := ruleResult{weights: map[string]quant.Decimal{}}
	ruleID := rule.Rule.ID
	availableSet := make(map[string]struct{}, len(sets.available))
	for _, id := range sets.available {
		availableSet[id] = struct{}{}
	}
	stops := newStopTracker(rule, frame, previous)
	entries := make(map[string]StopEntry)
	heldBefore := make(map[string]struct{}, len(previous.Held))
	held := make([]string, 0, len(previous.Held)+len(sets.available))
	// 冻结的持仓（因 K 线空洞缺数）不判 exit、不参与平分，但仍记在理论持仓里，保持结束后照常延续。
	var frozenHeld []string
	for _, id := range previous.Held {
		heldBefore[id] = struct{}{}
		if _, isFrozen := sets.frozen[id]; isFrozen {
			frozenHeld = append(frozenHeld, id)
			if entry, ok := previous.Entries[id]; ok {
				entries[id] = entry
			}
			continue
		}
		// 止损等同 exit，先于 exit 判断；只看价格，exit 的输入缺失不影响（设计 4.2）。
		if entry, ok := previous.Entries[id]; ok && stops != nil {
			updated, hit := stops.check(id, entry)
			if hit {
				result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageStopped, Reason: stops.stopped[id]})
				continue
			}
			entries[id] = updated
		}
		if _, ok := availableSet[id]; !ok {
			// exit 输入缺失的持仓按清仓处理；明细已由集合划分记录为 missing。
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
		if stops.excluded(id) {
			result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageDropped, Reason: reasonCooldown})
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
		result.items = append(result.items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageWeighted, Weight: truncateWeightWith(weight, tolerance).String()})
	}
	result.weights = weights
	result.state.Held = append(append([]string(nil), held...), frozenHeld...)
	sort.Strings(result.state.Held)
	if stops != nil {
		state := make(map[string]StopEntry, len(result.state.Held))
		for _, id := range result.state.Held {
			if entry, ok := entries[id]; ok {
				state[id] = entry
			} else if entry, ok := stops.open(id); ok {
				state[id] = entry
			}
		}
		if len(state) > 0 {
			result.state.Entries = state
		}
		result.state.Cooldown = stops.cooldownState()
		result.stops = stops.counts
	}
	return result, nil
}
