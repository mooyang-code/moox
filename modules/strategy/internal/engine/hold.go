package engine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// rank 规则的进出场持有（hold 块）。设计里的状态与代码的对应：frozen 是冻结保持阶段（StageHeld），其余是下面的原因码。
const (
	reasonHoldKept       = "hold_kept"
	reasonHoldEntered    = "hold_entered"
	reasonHoldNotEntered = "hold_not_entered"
	reasonHoldCooling    = "hold_cooling"
	reasonExitCondition  = "exit_condition"
	reasonExitMaxBars    = "exit_max_bars"
	reasonExitFilter     = "exit_filter"
	reasonExitMissing    = "exit_missing"
	reasonExitStop       = "exit_stop"

	// holdStateNoteBytes 是 hold 状态的观察阈值：超过时在本期摘要里说明（不截断）。
	holdStateNoteBytes = 256 << 10
)

// HoldState 是 hold 规则的跨期状态：持有标的的入场序号、冷却标的的到期序号（含），以及与日历同写时上一次判进出场的序号
// （与“最近一个应建仓的序号”比较，漏掉的建仓 bar 在之后第一根 ok bar 上补判）。持有集合本身记在 RuleState.Held。
// 状态为 nil 表示规则还没有求值过（启用后的第一根 ok bar，或停用再启用）。
type HoldState struct {
	Entered  map[string]int64 `json:"entered,omitempty"`
	Cooldown map[string]int64 `json:"cooldown,omitempty"`
	Decided  int64            `json:"decided,omitempty"`
	// Weights 是与日历同写时上一次判进出场定下的基准权重（持有标的 → 份额，预算按 1 计，含 cap）；非建仓 bar 沿用它配权。
	Weights map[string]string `json:"weights,omitempty"`
	// Groups 是写了 select.group 时持有标的最近一次已知的分组值：冻结且本根没有行时，用它占所属组的名额。
	Groups map[string]float64 `json:"groups,omitempty"`
}

// holdDecision 报告本根是否判 filter、exit、max_bars 与补位，并返回之后记下的“上次判进出场的序号”。
// 不与日历同写时每根都判；与日历同写时只在建仓 bar（含漏掉后的第一根 ok bar）判，首次求值也判。
// 不守门的一期（永续整体不可用时的空头规则）没有可用候选，不判，留到之后的 ok bar。
func holdDecision(rule *dsl.CompiledRule, frame Frame, sets ruleSets, prior *HoldState, first bool) (bool, int64) {
	if sets.waived {
		return false, prior.Decided
	}
	holding := rule.Rule.Holding
	if !holding.IsCalendar() {
		return true, 0
	}
	last, scheduled := frame.Holdings[rule.Rule.ID].Scheduled[holding.Days[0]]
	if !first && (!scheduled || prior.Decided >= last) {
		return false, prior.Decided
	}
	if scheduled {
		return true, last
	}
	return true, frame.BarIndex
}

// evaluateHold 执行进出场持有（设计 S2）：冻结 → 出场（止损优先、缺数、filter、exit、max_bars）→ 按名次补位 → 配权 → 状态。
func evaluateHold(rule *dsl.CompiledRule, frame Frame, sets ruleSets, previous RuleState, tolerance quant.Decimal) (ruleResult, error) {
	result := ruleResult{weights: map[string]quant.Decimal{}}
	explain := newExplanation(rule.Rule.ID, tolerance)
	hold := rule.Rule.Hold
	bar := frame.BarIndex
	first := previous.Hold == nil
	prior := previous.Hold
	if prior == nil {
		prior = &HoldState{}
	}
	decide, decided := holdDecision(rule, frame, sets, prior, first)
	stops := newStopTracker(rule, frame, previous)
	// filter 与 score：持有标的与候选一起算，截面函数在通过 filter 的样本上计算。
	filterOK := make(map[string]bool, len(sets.available))
	passed := make([]string, 0, len(sets.available))
	for _, id := range sets.available {
		ok := true
		if rule.Filter != nil {
			var err error
			if ok, err = runBool(rule.Filter, id, frame.Rows[id], 0); err != nil {
				return ruleResult{}, fmt.Errorf("filter 在 %s 上执行失败：%w", id, err)
			}
		}
		filterOK[id] = ok
		if ok {
			passed = append(passed, id)
		} else {
			result.filtered++
		}
	}
	scored := evaluateNumeric(rule.Score, passed, frame.Rows)
	live := without(passed, scored.failed)
	// 本根要补位时，未持有的候选分数全部无效说明表达式本身有问题；持有标的只缺分数不出场，不参与这项检查。
	// allow_empty 的规则不参与整期守门，这时只是不补位。
	if decide {
		candidates, valid := 0, 0
		for _, id := range passed {
			if contains(previous.Held, id) {
				continue
			}
			candidates++
			if _, failed := scored.failed[id]; !failed {
				valid++
			}
		}
		if candidates > 0 && valid == 0 && !rule.Rule.AllowEmpty {
			return ruleResult{}, fmt.Errorf("score 对全部 %d 个通过 filter 的候选都无效", candidates)
		}
	}
	result.scored = len(live)
	ordered := orderByRank(live, scored.values, rule.Rule.Select.UsesBottom())
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
	expected := make(map[string]struct{}, len(sets.expected))
	for _, id := range sets.expected {
		expected[id] = struct{}{}
	}
	cooldown := make(map[string]int64, len(prior.Cooldown))
	for id, until := range prior.Cooldown {
		if until >= bar {
			cooldown[id] = until
		}
	}
	entered := make(map[string]int64, len(previous.Held))
	entries := make(map[string]StopEntry)
	exited := make(map[string]struct{})
	exit := func(id string) {
		exited[id] = struct{}{}
		if !hold.AllowReentry {
			cooldown[id] = bar + int64(hold.CooldownBars)
		}
	}
	held := make([]string, 0, len(previous.Held))
	for _, id := range previous.Held {
		since, ok := prior.Entered[id]
		if !ok {
			since = bar
		}
		// 1. 冻结：不判出场，持有根数照常累计，占用名额。
		if _, isFrozen := sets.frozen[id]; isFrozen {
			held, entered[id] = append(held, id), since
			if entry, ok := previous.Entries[id]; ok {
				entries[id] = entry
			}
			continue
		}
		// 2. 出场。已不在预期集合里的（退市、移出标签）直接出场，明细由 notExpected 给出。
		if _, ok := expected[id]; !ok {
			exit(id)
			continue
		}
		// 止损每根都判，与其他原因同根成立时按止损记（冷却按 stop.cooldown_bars）。
		if entry, ok := previous.Entries[id]; ok && stops != nil {
			updated, hit := stops.check(id, entry)
			if hit {
				exited[id] = struct{}{}
				explain.items[id] = Item{RuleID: rule.Rule.ID, InstrumentID: id, Stage: StageStopped, Reason: reasonExitStop + ":" + stops.stopped[id]}
				continue
			}
			entries[id] = updated
		}
		if _, missing := sets.missing[id]; missing {
			exit(id)
			explain.items[id] = Item{RuleID: rule.Rule.ID, InstrumentID: id, Stage: StageMissing, Reason: reasonExitMissing}
			continue
		}
		if decide {
			reason := ""
			switch {
			case !filterOK[id]:
				reason = reasonExitFilter
			case rule.HoldExit != nil:
				out, err := runBool(rule.HoldExit, id, frame.Rows[id], 0)
				if err != nil {
					return ruleResult{}, fmt.Errorf("hold.exit 在 %s 上执行失败：%w", id, err)
				}
				if out {
					reason = reasonExitCondition
				}
			}
			if reason == "" && hold.MaxBars > 0 && bar-since >= int64(hold.MaxBars) {
				reason = reasonExitMaxBars
			}
			if reason != "" {
				exit(id)
				explain.reject(id, StageDropped, reason, scoreOf(id), rankOf[id])
				continue
			}
		}
		held, entered[id] = append(held, id), since
	}
	heldSet := make(map[string]struct{}, len(held))
	for _, id := range held {
		heldSet[id] = struct{}{}
	}
	// 3. 补位：空位 = 目标数 − 留下的持有数（含冻结），不为负；持有数超过目标时不裁减。
	fresh := make(map[string]struct{})
	notChosen := reasonNotRebalanced
	if decide {
		notChosen = reasonNotSelected
		target := rule.Rule.Select.Count(len(live))
		if note := capShortNote(rule, target); note != "" {
			result.notes = append(result.notes, note)
		}
		quota := newGroupQuota(rule.Rule.Select.Group, append(append([]string(nil), ordered...), sortedMissingFromRank(held, rankOf)...), frame.Rows, frozenGroups(prior.Groups, sets.frozen))
		for _, id := range rankedFirst(held, rankOf) {
			quota.admit(id) // 持有标的先占各组名额
		}
		free := target - len(held)
		for _, id := range ordered {
			if _, ok := heldSet[id]; ok {
				continue
			}
			if _, ok := exited[id]; ok && !hold.AllowReentry {
				continue
			}
			reason, stage := "", StageScored
			switch {
			case stops.excluded(id):
				reason, stage = reasonCooldown, StageDropped
			case inCooldown(cooldown, id, bar):
				reason, stage = reasonHoldCooling, StageIdle
			case free <= 0:
				reason = reasonNotSelected
			}
			if reason == "" && rule.SelectWhere != nil {
				ok, err := runBool(rule.SelectWhere, id, frame.Rows[id], scored.values[id])
				if err != nil {
					return ruleResult{}, fmt.Errorf("select.where 在 %s 上执行失败：%w", id, err)
				}
				if !ok {
					reason = "select.where"
				}
			}
			if reason == "" {
				ok, err := holdCanEnter(rule, id, frame.Rows[id], hold.AllowReentry)
				if err != nil {
					return ruleResult{}, err
				}
				if !ok {
					reason, stage = reasonHoldNotEntered, StageIdle
				}
			}
			if reason == "" {
				reason = quota.admit(id)
			}
			if reason != "" {
				explain.reject(id, stage, reason, scoreOf(id), rankOf[id])
				continue
			}
			held, entered[id] = append(held, id), bar
			heldSet[id] = struct{}{}
			fresh[id] = struct{}{}
			free--
		}
	}
	for _, id := range sets.available {
		if _, ok := explain.items[id]; ok {
			continue
		}
		if _, ok := heldSet[id]; ok {
			continue
		}
		switch {
		case !filterOK[id]:
			explain.reject(id, StageFiltered, "filter", nil, 0)
		case scored.failed[id] != "":
			explain.reject(id, StageDropped, scored.failed[id], nil, 0)
		default:
			explain.reject(id, StageScored, notChosen, scoreOf(id), rankOf[id])
		}
	}
	result.selected = len(held)
	// 4. 配权：对持有集合（含冻结）按 weight 配权；冻结标的的份额由组合层保持上期目标，这里不输出。
	// 与日历同写时，配权与 holding 批次同一口径：建仓 bar（判进出场的 bar）按份额（预算按 1 计，含 cap）定下基准权重并记进状态，
	// 其余 bar 沿用基准权重，不按本根名次重新配权；出场空出的份额留现金，到下一个建仓 bar 再分。
	var weights map[string]quant.Decimal
	var base map[string]string
	if rule.Rule.Holding.IsCalendar() {
		var err error
		weights, base, err = holdCalendarWeights(rule, held, rankOf, decide, prior)
		if err != nil {
			return ruleResult{}, err
		}
	} else {
		weights = allocate(rankedFirst(held, rankOf), rule.Rule.Weight)
	}
	short := rule.Rule.Side == dsl.SideShort
	for _, id := range sortedIDs(weights) {
		if _, isFrozen := sets.frozen[id]; isFrozen {
			delete(weights, id)
			continue
		}
		weight := weights[id]
		if weight.IsZero() {
			delete(weights, id)
			continue
		}
		if short {
			weight = weight.Neg()
			weights[id] = weight
		}
		explain.weight(id, weight, scoreOf(id), rankOf[id], true)
		item := explain.items[id]
		item.Reason = reasonHoldKept
		if _, ok := fresh[id]; ok {
			item.Reason = reasonHoldEntered
		}
		explain.items[id] = item
	}
	result.weights = weights
	// 5. 状态。
	sort.Strings(held)
	result.state.Held = held
	state := &HoldState{Decided: decided, Weights: base}
	if group := rule.Rule.Select.Group; group != nil {
		for _, id := range held {
			if value, ok := groupValue(group, id, frame.Rows, frozenGroups(prior.Groups, sets.frozen)); ok {
				if state.Groups == nil {
					state.Groups = make(map[string]float64, len(held))
				}
				state.Groups[id] = value
			}
		}
	}
	if len(entered) > 0 {
		state.Entered = make(map[string]int64, len(held))
		for _, id := range held {
			state.Entered[id] = entered[id]
		}
	}
	for id, until := range cooldown {
		if until > bar {
			if state.Cooldown == nil {
				state.Cooldown = make(map[string]int64)
			}
			state.Cooldown[id] = until
		}
	}
	result.state.Hold = state
	if stops != nil {
		open := make(map[string]StopEntry, len(held))
		for _, id := range held {
			if entry, ok := entries[id]; ok {
				open[id] = entry
			} else if entry, ok := stops.open(id); ok {
				open[id] = entry
			}
		}
		if len(open) > 0 {
			result.state.Entries = open
		}
		result.state.Cooldown = stops.cooldownState()
		result.stops = stops.counts
	}
	if raw, err := json.Marshal(state); err == nil && len(raw) > holdStateNoteBytes {
		result.notes = append(result.notes, fmt.Sprintf("规则 %s 的 hold 状态有 %d 字节，超过 %d 字节", rule.Rule.ID, len(raw), holdStateNoteBytes))
	}
	result.items = explain.list()
	return result, nil
}

// holdCalendarWeights 返回与日历同写时本根的权重与要记进状态的基准份额。判进出场的 bar 上按持有集合重新定份额；
// 其余 bar 沿用上次的份额，只保留仍在持有里的标的（出场的份额留现金）。
func holdCalendarWeights(rule *dsl.CompiledRule, held []string, rankOf map[string]int, decide bool, prior *HoldState) (map[string]quant.Decimal, map[string]string, error) {
	weight := rule.Rule.Weight
	base := make(map[string]string, len(held))
	if decide || prior.Weights == nil {
		order := rankedFirst(held, rankOf)
		shares := baseShares(order, quant.One(), weight.Method)
		if weight.HasCap {
			shares = applyCapRat(order, shares, new(big.Rat).Quo(weight.Cap.Rat(), weight.Total.Rat()))
		}
		for id, share := range shares {
			base[id] = share.String()
		}
	} else {
		for _, id := range held {
			if raw, ok := prior.Weights[id]; ok {
				base[id] = raw
			}
		}
	}
	weights := make(map[string]quant.Decimal, len(base))
	for id, raw := range base {
		share, err := quant.Parse(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("hold 状态里 %s 的基准权重无效：%w", id, err)
		}
		weights[id] = share.Mul(weight.Total)
	}
	if len(base) == 0 {
		base = nil
	}
	return weights, base, nil
}

// frozenGroups 只保留冻结标的记下的分组值：冻结且本根没有行的持仓沿用它占名额；本根有行、分组值为非数的持仓仍按 group_none 处理。
func frozenGroups(groups map[string]float64, frozen map[string]struct{}) map[string]float64 {
	if len(groups) == 0 || len(frozen) == 0 {
		return nil
	}
	out := make(map[string]float64, len(frozen))
	for id := range frozen {
		if value, ok := groups[id]; ok {
			out[id] = value
		}
	}
	return out
}

// holdCanEnter 判断候选能否入场：entry 为真（不写表示不限制），且 exit 不为真（同一根两者都成立时不入场）。
func holdCanEnter(rule *dsl.CompiledRule, id string, row Row, allowReentry bool) (bool, error) {
	if rule.HoldEntry != nil {
		ok, err := runBool(rule.HoldEntry, id, row, 0)
		if err != nil {
			return false, fmt.Errorf("hold.entry 在 %s 上执行失败：%w", id, err)
		}
		if !ok {
			return false, nil
		}
	}
	if rule.HoldExit != nil && !allowReentry {
		out, err := runBool(rule.HoldExit, id, row, 0)
		if err != nil {
			return false, fmt.Errorf("hold.exit 在 %s 上执行失败：%w", id, err)
		}
		return !out, nil
	}
	return true, nil
}

// rankedFirst 把标的按本期名次排序，没有名次的（冻结、分数缺失）排在后面并按 id 排序：用于持有集合的组名额与 rank 配权。
func rankedFirst(ids []string, rankOf map[string]int) []string {
	out := append([]string(nil), ids...)
	sort.SliceStable(out, func(i, j int) bool {
		a, okA := rankOf[out[i]]
		b, okB := rankOf[out[j]]
		switch {
		case okA && okB:
			return a < b
		case okA != okB:
			return okA
		}
		return out[i] < out[j]
	})
	return out
}

// sortedMissingFromRank 返回 ids 里没有名次的标的（按 id 排序）：它们也要能取到分组值，参与组名额。
func sortedMissingFromRank(ids []string, rankOf map[string]int) []string {
	var out []string
	for _, id := range ids {
		if _, ok := rankOf[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
