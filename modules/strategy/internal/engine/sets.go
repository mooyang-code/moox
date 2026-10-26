package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// ruleSets 是一条规则的集合划分：预期 E(r)、年龄剔除 G(r)、可用 A(r)、缺数 M(r)，以及保持持仓的冻结集合 F(r)。
// 冻结的标的（因 K 线空洞缺数的已持仓标的，见 freeze.go）既不算可用也不算缺数：规则对它们不求值，持仓保持不动；
// 但守门仍把它们计入缺数比例，保护中的标的不能伪装成可用。
type ruleSets struct {
	expected  []string
	agedOut   map[string]struct{}
	available []string
	missing   map[string]string
	frozen    map[string]struct{}
	// inactive 是 f1 规则里本期有行但 volume 为 0 的标的：不是候选，也不算缺数（F1 只保留 volume > 0 的行）。
	inactive []string
}

// partitionRule 按规则引用的列划分可用与缺数：行不存在、所需列为空、或该列上游对该标的失败，都算缺数。
// 年龄剔除只拦住新进入的标的：规则上期已持有的标的在建仓时已经满足 min_age_bars，不会因为探针窗口内恰好
// 缺数（历史数据缺口）被当作新上市剔除。signal 规则的持仓只判 exit，只需要 exit 引用的列（含 bars[-1]）：
// 只缺 entry 用到的输入（例如交叉入场的上一根）不能触发清仓；未持有的候选同时判 entry 与 exit，需要全部列。
func partitionRule(rule *dsl.CompiledRule, frame Frame, held []string, frozen map[string]struct{}) ruleSets {
	sets := ruleSets{agedOut: make(map[string]struct{}), missing: make(map[string]string), frozen: make(map[string]struct{})}
	heldSet := make(map[string]struct{}, len(held))
	for _, id := range held {
		heldSet[id] = struct{}{}
	}
	for _, id := range frame.AgedOut[rule.Rule.ID] {
		if _, isHeld := heldSet[id]; isHeld {
			continue
		}
		sets.agedOut[id] = struct{}{}
	}
	expected := append([]string(nil), frame.Expected[rule.Rule.ID]...)
	sort.Strings(expected)
	sets.expected = expected
	columns := rule.Columns()
	previousColumns := rule.PreviousColumns()
	heldColumns, heldPrevious := columns, previousColumns
	if rule.Rule.Type == dsl.RuleTypeSignal && rule.Exit != nil {
		heldColumns, heldPrevious = rule.Exit.AllColumns(), rule.Exit.AllPreviousColumns()
	}
	for _, id := range expected {
		if _, aged := sets.agedOut[id]; aged {
			continue
		}
		if _, isFrozen := frozen[id]; isFrozen {
			sets.frozen[id] = struct{}{}
			continue
		}
		row, ok := frame.Rows[id]
		if !ok {
			sets.missing[id] = "no_row"
			continue
		}
		current, previous := columns, previousColumns
		if _, isHeld := heldSet[id]; isHeld {
			current, previous = heldColumns, heldPrevious
		}
		if reason := missingReason(id, row, current, previous, frame.FailedColumns); reason != "" {
			sets.missing[id] = reason
			continue
		}
		sets.available = append(sets.available, id)
	}
	return sets
}

// missingReason 返回标的在检查列上的第一个缺失原因（空串表示不缺）；明细只记这一个。
func missingReason(id string, row Row, columns, previousColumns []string, failed map[string]map[string]string) string {
	causes := missingCauses(id, row, true, columns, previousColumns, failed)
	if len(causes) == 0 {
		return ""
	}
	return causes[0].text
}

// items 为年龄剔除、缺数与保持持仓的标的生成解释明细；其余标的的明细由各规则求值产生。
func (s ruleSets) items(ruleID string) []Item {
	items := make([]Item, 0, len(s.agedOut)+len(s.missing)+len(s.frozen))
	for _, id := range s.expected {
		if _, aged := s.agedOut[id]; aged {
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageAgedOut, Reason: "min_age_bars"})
			continue
		}
		if _, held := s.frozen[id]; held {
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageHeld, Reason: reasonGapHold})
			continue
		}
		if reason, missing := s.missing[id]; missing {
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageMissing, Reason: reason})
		}
	}
	return items
}

// unevaluated 为整期守门跳过时尚未求值的可用标的生成明细，使每条规则的解释仍覆盖全部 E(r)。
func (s ruleSets) unevaluated(ruleID string) []Item {
	items := make([]Item, 0, len(s.available))
	for _, id := range s.available {
		items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageIdle, Reason: reasonPeriodSkipped})
	}
	return items
}

// notExpected 为上期持有、本期已不在预期集合中的标的生成 dropped 明细（例如退市或移出标签）。
// 在预期集合中但缺数或年龄不足的标的已有各自的明细。
func (s ruleSets) notExpected(ruleID string, held []string) []Item {
	expected := make(map[string]struct{}, len(s.expected))
	for _, id := range s.expected {
		expected[id] = struct{}{}
	}
	var items []Item
	for _, id := range held {
		if _, ok := expected[id]; !ok {
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageDropped, Reason: reasonNotExpected})
		}
	}
	return items
}
