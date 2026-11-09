package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 本文件实现规则级市场（规则写了 market: spot | swap，策略涉及现货、永续两个市场时生效）：
//   - 这类规则不经过选币池构造，改用只含该市场行与失败证据的视图（ruleMarketViews），目标也落在该市场；
//   - “上期持有沿用市场”不作用于它们，它们的持仓也不让没写规则级市场的规则沿用（unmarkedHeld）；
//   - 冻结按（标的, 市场）键：用该市场的视图、只在写了这个市场的规则之间合并（frozenSet 的 scope），组合层只保持那一个键；
//   - 缺数与年龄按规则所用的市场划分：年龄由输入层只看该市场的判定（input.AgeRule.Market），视图不再按 MarketInput.AgedOut 剔行。
// 没有规则写规则级市场时，这里的函数都不改变任何结果。

// ruleMarketViews 为写了规则级市场的规则构造各市场的视图（市场 → 视图）：Rows 与 FailedColumns 取该市场本期的行与失败证据，
// 规则的预期集合收窄到在这个市场有行、有失败证据或本规则上期持有的标的（基础集合是两个市场的并集，只在另一个市场的标的不是这条规则的候选，
// 不能记成无行缺数），年龄剔除集合随之收窄。没有这类规则时返回 nil。
func ruleMarketViews(program *dsl.Program, mode dsl.Market, frame Frame, previous State) map[string]Frame {
	var views map[string]Frame
	for i := range program.Rules {
		rule := program.Rules[i].Rule
		market := dsl.RuleMarket(mode, rule)
		if market == "" {
			continue
		}
		if views == nil {
			views = make(map[string]Frame, 2)
		}
		input := frame.Markets[market]
		view, ok := views[market]
		if !ok {
			view = frame
			view.Rows, view.FailedColumns = input.Rows, input.Failed
			view.RowMarket, view.avail, view.shortNoRow = nil, nil, nil
			view.Expected, view.AgedOut = map[string][]string{}, map[string][]string{}
			views[market] = view
		}
		evidence := make(map[string]struct{})
		for _, subjects := range input.Failed {
			for id := range subjects {
				evidence[id] = struct{}{}
			}
		}
		heldByRule := previous.Rules[rule.ID].Held
		kept := make(map[string]struct{}, len(frame.Expected[rule.ID]))
		expected := make([]string, 0, len(frame.Expected[rule.ID]))
		for _, id := range frame.Expected[rule.ID] {
			_, hasRow := input.Rows[id]
			_, failed := evidence[id]
			if hasRow || failed || contains(heldByRule, id) {
				expected = append(expected, id)
				kept[id] = struct{}{}
			}
		}
		aged := make([]string, 0, len(frame.AgedOut[rule.ID]))
		for _, id := range frame.AgedOut[rule.ID] {
			if _, ok := kept[id]; ok {
				aged = append(aged, id)
			}
		}
		view.Expected[rule.ID], view.AgedOut[rule.ID] = expected, aged
	}
	return views
}

// frameFor 返回规则求值用的帧：写了规则级市场的规则用该市场的视图，其余用选币池构造后的帧。
func frameFor(mode dsl.Market, rule dsl.Rule, frame Frame, views map[string]Frame) Frame {
	if market := dsl.RuleMarket(mode, rule); market != "" {
		return views[market]
	}
	return frame
}

// unmarkedHeld 返回没写规则级市场的规则用的上期持仓索引（选币池构造的粘住、空头候选过滤、按币冻结与定市场都用它）：
// 去掉只被写了规则级市场的规则持有的标的，这些持仓不让其他规则沿用市场。同一个币同时被两类规则持有时，聚合后的目标分不开，整份保留。
// 没有规则级市场时原样返回。
func unmarkedHeld(program *dsl.Program, mode dsl.Market, previous State, held heldIndex) heldIndex {
	marked := false
	unmarked := make(map[string]struct{})
	for i := range program.Rules {
		rule := program.Rules[i].Rule
		if dsl.RuleMarket(mode, rule) != "" {
			marked = true
			continue
		}
		for _, id := range previous.Rules[rule.ID].Held {
			unmarked[id] = struct{}{}
		}
	}
	if !marked {
		return held
	}
	markedHeld := make(map[string]struct{})
	for i := range program.Rules {
		rule := program.Rules[i].Rule
		if dsl.RuleMarket(mode, rule) == "" {
			continue
		}
		for _, id := range previous.Rules[rule.ID].Held {
			markedHeld[id] = struct{}{}
		}
	}
	filtered := make(heldIndex, len(held))
	for id, entries := range held {
		_, byMarked := markedHeld[id]
		if _, byUnmarked := unmarked[id]; byUnmarked || !byMarked {
			filtered[id] = entries
		}
	}
	return filtered
}

// subjectsIn 返回上期在这个市场持有非零目标的标的（排序）：按键冻结的候选。
func (h heldIndex) subjectsIn(market string) []string {
	var ids []string
	for id := range h {
		if h.holds(id, market) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
