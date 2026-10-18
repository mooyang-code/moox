package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 缺失原因的分类：决定已持仓标的缺数时是保持持仓还是按缺数处理。
const (
	// causeGap：上游因子对该标的因回看窗口 K 线空洞失败（FailedGap）。
	causeGap = "gap"
	// causePrevOnly：当期输入齐全，只缺上一根（bars[-1]）的值，且没有失败证据。
	causePrevOnly = "prevonly"
	// causeOther：其余一切缺失（因子失败、K 线失败、无行、值缺失而没有证据）。
	causeOther = "other"

	reasonGapHold = "gap_hold"
)

// cause 是一列上的一个缺失原因。text 与明细里的 Reason 一致（例如 factor_gap:ma、missing:bars[-1].ma）。
type cause struct {
	kind string
	text string
}

// missingCauses 逐列收集标的在检查列上的全部缺失原因（顺序与 missingReason 的判定顺序一致）。hasRow 为 false 表示
// 该标的当期没有行：逐列判断，有失败证据的列按证据归类（空洞造成的无行仍是 gap），没有证据的必需列记 no_row（other）。
func missingCauses(id string, row Row, hasRow bool, columns, previousColumns []string, failed map[string]map[string]string) []cause {
	var causes []cause
	classify := func(reason string) string {
		if reason == FailedGap {
			return causeGap
		}
		return causeOther
	}
	for _, column := range columns {
		if reason, failedSubject := failed[column][id]; failedSubject {
			causes = append(causes, cause{kind: classify(reason), text: reason + ":" + column})
			continue
		}
		if !hasRow {
			causes = append(causes, cause{kind: causeOther, text: "no_row"})
			continue
		}
		if _, ok := row.Values[column]; !ok {
			causes = append(causes, cause{kind: causeOther, text: "missing:" + column})
		}
	}
	for _, column := range previousColumns {
		// 经 bars[-1] 引用的因子同样属于该规则引用的因子：本期上游对该标的失败，整条规则对它视为缺数。
		if reason, failedSubject := failed[column][id]; failedSubject {
			causes = append(causes, cause{kind: classify(reason), text: reason + ":bars[-1]." + column})
			continue
		}
		if !hasRow {
			continue // 无行已由当期列记录
		}
		if _, ok := row.Previous[column]; !ok {
			causes = append(causes, cause{kind: causePrevOnly, text: "missing:bars[-1]." + column})
		}
	}
	return causes
}

// heldColumns 是规则对「该规则已持有的标的」检查的列：signal 规则的持仓只判 exit，只需要 exit 引用的列。
func heldColumns(rule *dsl.CompiledRule) (columns, previous []string) {
	columns, previous = rule.Columns(), rule.PreviousColumns()
	if rule.Rule.Type == dsl.RuleTypeSignal && rule.Exit != nil {
		columns, previous = rule.Exit.AllColumns(), rule.Exit.AllPreviousColumns()
	}
	return columns, previous
}

// frozenSet 返回因 K 线空洞缺数、需要保持持仓的标的：策略当前持有（上期组合目标非零）的标的，只要在所有引用它的规则里
// 缺失的原因全部是空洞（gap）或只缺上一根（prevonly），就冻结——持仓数量不动，不判 exit、不从目标里移除。只要有任何一条规则里
// 混有其他原因（因子失败、K 线失败、无行、值缺失而没有证据），就完全按现有逻辑处理。
func frozenSet(program *dsl.Program, frame Frame, previous State) map[string]struct{} {
	frozen := make(map[string]struct{})
	candidates := make([]string, 0, len(previous.Targets))
	for id, raw := range previous.Targets {
		weight, err := quant.Parse(raw)
		if err != nil || weight.IsZero() {
			continue
		}
		candidates = append(candidates, id)
	}
	sort.Strings(candidates)
	for _, id := range candidates {
		protectable, blocked := false, false
		for i := range program.Rules {
			rule := &program.Rules[i]
			ruleID := rule.Rule.ID
			if !contains(frame.Expected[ruleID], id) {
				continue
			}
			ruleHeld := contains(previous.Rules[ruleID].Held, id)
			if !ruleHeld && contains(frame.AgedOut[ruleID], id) {
				continue // 年龄剔除只拦新进入的标的
			}
			columns, previousColumns := rule.Columns(), rule.PreviousColumns()
			if ruleHeld {
				columns, previousColumns = heldColumns(rule)
			}
			row, hasRow := frame.Rows[id]
			for _, c := range missingCauses(id, row, hasRow, columns, previousColumns, frame.FailedColumns) {
				switch c.kind {
				case causeOther:
					blocked = true
				default:
					protectable = true
				}
			}
		}
		if protectable && !blocked {
			frozen[id] = struct{}{}
		}
	}
	return frozen
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
