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

// partitionRule 按规则引用的列划分可用与缺数：行不存在、所需列为空、或该列所属因子对该标的失败，都算缺数。
func partitionRule(rule *dsl.CompiledRule, frame Frame) ruleSets {
	sets := ruleSets{agedOut: make(map[string]struct{}), missing: make(map[string]string)}
	for _, id := range frame.AgedOut[rule.Rule.ID] {
		sets.agedOut[id] = struct{}{}
	}
	expected := append([]string(nil), frame.Expected[rule.Rule.ID]...)
	sort.Strings(expected)
	sets.expected = expected
	columns := rule.Columns()
	previousColumns := rule.PreviousColumns()
	for _, id := range expected {
		if _, aged := sets.agedOut[id]; aged {
			continue
		}
		row, ok := frame.Rows[id]
		if !ok {
			sets.missing[id] = "no_row"
			continue
		}
		if reason := missingReason(id, row, columns, previousColumns, frame.FailedColumns); reason != "" {
			sets.missing[id] = reason
			continue
		}
		sets.available = append(sets.available, id)
	}
	return sets
}

func missingReason(id string, row Row, columns, previousColumns []string, failed map[string]map[string]struct{}) string {
	for _, column := range columns {
		if subjects, ok := failed[column]; ok {
			if _, failedSubject := subjects[id]; failedSubject {
				return "factor_failed:" + column
			}
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
