package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// ruleSets 是一条规则的集合划分：预期 E(r)、年龄剔除 G(r)、可用 A(r)、缺数 M(r)。
type ruleSets struct {
	expected  []string
	agedOut   map[string]struct{}
	available []string
	missing   map[string]string
}

// partitionRule 按规则引用的列划分可用与缺数：行不存在、所需列为空、或该列上游对该标的失败，都算缺数。
// 年龄剔除只拦住新进入的标的：规则上期已持有的标的在建仓时已经满足 min_age_bars，不会因为探针窗口内恰好
// 缺数（历史数据缺口）被当作新上市剔除。signal 规则的持仓只判 exit，只需要 exit 引用的列（含 bars[-1]）：
// 只缺 entry 用到的输入（例如交叉入场的上一根）不能触发清仓；未持有的候选同时判 entry 与 exit，需要全部列。
func partitionRule(rule *dsl.CompiledRule, frame Frame, held []string) ruleSets {
	sets := ruleSets{agedOut: make(map[string]struct{}), missing: make(map[string]string)}
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

func missingReason(id string, row Row, columns, previousColumns []string, failed map[string]map[string]string) string {
	for _, column := range columns {
		if reason, failedSubject := failed[column][id]; failedSubject {
			return reason + ":" + column
		}
		if _, ok := row.Values[column]; !ok {
			return "missing:" + column
		}
	}
	for _, column := range previousColumns {
		if _, ok := row.Previous[column]; !ok {
			return "missing:bars[-1]." + column
		}
	}
	return ""
}

// items 为年龄剔除与缺数的标的生成解释明细；其余标的的明细由各规则求值产生。
func (s ruleSets) items(ruleID string) []Item {
	items := make([]Item, 0, len(s.agedOut)+len(s.missing))
	for _, id := range s.expected {
		if _, aged := s.agedOut[id]; aged {
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageAgedOut, Reason: "min_age_bars"})
			continue
		}
		if reason, missing := s.missing[id]; missing {
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageMissing, Reason: reason})
		}
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
