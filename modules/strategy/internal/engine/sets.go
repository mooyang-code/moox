package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// ruleSets 是一条规则的集合划分：预期 E(r)、年龄剔除 G(r)、可用 A(r)、缺数 M(r)，以及保持持仓的冻结集合 F(r)。
// 冻结的标的（因市场不可用或只缺上一根而缺数的已持仓标的，见 freeze.go）既不算可用也不算缺数：规则对它们不求值，持仓保持不动；
// 但守门仍把它们计入缺数比例，保护中的标的不能伪装成可用。
type ruleSets struct {
	expected []string
	agedOut  map[string]struct{}
	// agedOutReason 是 agedOut 里不按 min_age_bars 剔除的标的的明细原因（见 Frame.AgedOutReasons）。
	agedOutReason map[string]string
	available     []string
	missing       map[string]string
	frozen        map[string]struct{}
	// holdReason 是冻结标的的明细原因（prev_hold 或 market_hold，见 frozenSet）。
	holdReason map[string]string
	// waived 表示规则本期不守门（永续整体不可用时的空头规则，见 waivesShortGuard）：规则只保持冻结的持仓，holding 规则不重建批次。
	waived bool
}

// partitionRule 按规则引用的列划分可用与缺数：行不存在、所需列为空、或该列上游对该标的失败，都算缺数。
// 年龄剔除只拦住新进入的标的：规则上期已持有的标的在建仓时已经满足 min_age_bars，不会因为探针窗口内恰好
// 缺数（历史数据缺口）被当作新上市剔除。持有的标的只检查 heldColumns 给出的列（见 freeze.go）。
func partitionRule(rule *dsl.CompiledRule, frame Frame, held []string, frozen map[string]struct{}, holdReasons map[string]string) ruleSets {
	sets := ruleSets{agedOut: make(map[string]struct{}), missing: make(map[string]string), frozen: make(map[string]struct{}), holdReason: holdReasons}
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
	sets.agedOutReason = frame.AgedOutReasons[rule.Rule.ID]
	expected := append([]string(nil), frame.Expected[rule.Rule.ID]...)
	sort.Strings(expected)
	sets.expected = expected
	columns := rule.Columns()
	previousColumns := rule.PreviousColumns()
	heldCurrent, heldPrevious := heldColumns(rule)
	for _, id := range expected {
		if _, aged := sets.agedOut[id]; aged {
			continue
		}
		if _, isFrozen := frozen[id]; isFrozen {
			sets.frozen[id] = struct{}{}
			continue
		}
		row, ok := frame.Rows[id]
		if _, noRow := frame.shortNoRow[rule.Rule.ID][id]; !ok || noRow {
			sets.missing[id] = "no_row"
			continue
		}
		current, previous := columns, previousColumns
		if _, isHeld := heldSet[id]; isHeld {
			current, previous = heldCurrent, heldPrevious
		}
		if reason := missingReason(id, row, current, previous, frame.FailedColumns); reason != "" {
			sets.missing[id] = reason
			continue
		}
		if _, valid := scaleOf(rule, id, row); !valid {
			sets.missing[id] = reasonScaleInvalid
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

// 年龄剔除的明细原因：ReasonAgedOutMax 是已通过 max_age_bars、不再算新标的；ReasonAgeMarkets 是 age_markets: all 的规则里
// 标的没在全部绑定市场列出，或某个市场本期整体不可用。
const (
	ReasonAgedOutMax = "aged_out_max"
	ReasonAgeMarkets = "age_markets"
)

// items 为年龄剔除、缺数与保持持仓的标的生成解释明细；其余标的的明细由各规则求值产生。
func (s ruleSets) items(ruleID string) []Item {
	items := make([]Item, 0, len(s.agedOut)+len(s.missing)+len(s.frozen))
	for _, id := range s.expected {
		if _, aged := s.agedOut[id]; aged {
			reason := "min_age_bars"
			if special, ok := s.agedOutReason[id]; ok {
				reason = special
			}
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageAgedOut, Reason: reason})
			continue
		}
		if _, held := s.frozen[id]; held {
			reason := s.holdReason[id]
			if reason == "" {
				reason = reasonPrevHold
			}
			items = append(items, Item{RuleID: ruleID, InstrumentID: id, Stage: StageHeld, Reason: reason})
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
