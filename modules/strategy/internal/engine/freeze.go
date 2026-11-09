package engine

import (
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 缺失原因的分类：决定已持仓标的缺数时是保持持仓还是按缺数处理。
const (
	// causeMarket：标的所在市场本期整体不可用（那个市场的因子本期被跳过）：已持仓的标的冻结。
	causeMarket = "market"
	// causePrevOnly：当期输入齐全，只缺上一根（bars[-1]）的值，且没有失败证据。
	causePrevOnly = "prevonly"
	// causeOther：其余一切缺失（因子失败、K 线失败、无行、值缺失而没有证据）。
	causeOther = "other"

	// 冻结标的的明细原因：reasonPrevHold 是只缺上一根保持持仓，reasonMarketHold 是所在市场本期整体不可用保持持仓。
	reasonPrevHold   = "prev_hold"
	reasonMarketHold = "market_hold"
)

// cause 是一列上的一个缺失原因。text 与明细里的 Reason 一致（例如 market_unavailable:ma、missing:bars[-1].ma）。
type cause struct {
	kind string
	text string
}

// missingCauses 逐列收集标的在检查列上的全部缺失原因（顺序与 missingReason 的判定顺序一致）。hasRow 为 false 表示
// 该标的当期没有行：逐列判断，有失败证据的列按证据归类（市场不可用造成的无行仍是 market），没有证据的必需列记 no_row（other）。
func missingCauses(id string, row Row, hasRow bool, columns, previousColumns []string, failed map[string]map[string]string) []cause {
	var causes []cause
	classify := func(reason string) string {
		if reason == FailedMarket {
			return causeMarket
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

// heldColumns 是规则对「该规则已持有的标的」检查的列：signal 规则的持仓只判 exit，只需要 exit（与 scale）引用的列；
// hold 规则的持仓只看 filter、hold.exit、scale 与 select.group.by，score、entry 缺值时不出场，只是不能参与补位。
// 集合划分与冻结判定共用这一个口径。
func heldColumns(rule *dsl.CompiledRule) (columns, previous []string) {
	var expressions []*dsl.Expression
	switch {
	case rule.Rule.Type == dsl.RuleTypeSignal && rule.Exit != nil:
		expressions = []*dsl.Expression{rule.Exit, rule.Scale}
	case rule.Rule.Hold != nil:
		expressions = []*dsl.Expression{rule.Filter, rule.HoldExit, rule.Scale}
	default:
		return rule.Columns(), rule.PreviousColumns()
	}
	{
		current, prior := make(map[string]struct{}), make(map[string]struct{})
		for _, expression := range expressions {
			if expression == nil {
				continue
			}
			for _, column := range expression.AllColumns() {
				current[column] = struct{}{}
			}
			for _, column := range expression.AllPreviousColumns() {
				prior[column] = struct{}{}
			}
		}
		if rule.Rule.Hold != nil {
			for _, column := range rule.Rule.DirectColumns() {
				current[column] = struct{}{}
			}
		}
		return sortedSet(current), sortedSet(prior)
	}
}

func sortedSet(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// frozenSet 返回因市场不可用或只缺上一根而缺数、需要保持持仓的标的：策略当前持有（上期组合目标非零，任一市场）的标的，
// 只要在所有引用它的规则里缺失的原因全部是市场不可用（market）或只缺上一根（prevonly），就冻结——持仓数量不动，不判 exit、不从目标里移除。
// 只要有任何一条规则里混有其他原因（因子失败、K 线失败、无行、值缺失而没有证据），就完全按现有逻辑处理。
// reasons 是每个冻结标的的明细原因：任一规则里有市场不可用的原因记 market_hold，否则记 prev_hold。
//
// 策略里有规则写了 on_missing 时按另一套合并规则（见 frozenSetOnMissing）；没有时逻辑与结果都不变。streaks 是各规则新的连续缺数根数。
func frozenSet(program *dsl.Program, frame Frame, previous State, held heldIndex) (frozen map[string]struct{}, reasons map[string]string, streaks map[string]map[string]int) {
	frozen, reasons = make(map[string]struct{}), make(map[string]string)
	// tranche 规则不与其他规则混用，持仓到期由自己的持有计数决定，不参与冻结。
	if program.Strategy.HasTranche() {
		return frozen, reasons, nil
	}
	if program.Strategy.HasOnMissing() {
		return frozenSetOnMissing(program, frame, previous, held)
	}
	candidates := held.subjects()
	for _, id := range candidates {
		protectable, blocked, market := false, false, false
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
			if _, noRow := frame.shortNoRow[ruleID][id]; noRow {
				blocked = true // 永续已经没有它的空头：按无行处理，不借用另一个市场的证据冻结
				continue
			}
			for _, c := range ruleCauses(rule, frame, id, ruleHeld) {
				switch c.kind {
				case causeOther:
					blocked = true
				case causeMarket:
					protectable, market = true, true
				default:
					protectable = true
				}
			}
		}
		if protectable && !blocked {
			frozen[id] = struct{}{}
			reasons[id] = reasonPrevHold
			if market {
				reasons[id] = reasonMarketHold
			}
		}
	}
	return frozen, reasons, nil
}

// ruleCauses 返回标的在一条规则里的缺失原因：持有它的规则按 heldColumns 检查；仓位系数无效也算其他原因。
func ruleCauses(rule *dsl.CompiledRule, frame Frame, id string, ruleHeld bool) []cause {
	columns, previousColumns := rule.Columns(), rule.PreviousColumns()
	if ruleHeld {
		columns, previousColumns = heldColumns(rule)
	}
	row, hasRow := frame.Rows[id]
	causes := missingCauses(id, row, hasRow, columns, previousColumns, frame.FailedColumns)
	// hold 规则持有时检查的列可能为空（不写 filter、exit、scale、group 时）：没有行时先按规则的全部列找失败证据（市场不可用记 market），
	// 没有证据才记 no_row，与集合划分的口径一致。其他规则不走这里，结果与以前逐位一致。
	if len(causes) == 0 && !hasRow && rule.Rule.Hold != nil {
		causes = missingCauses(id, row, hasRow, rule.Columns(), rule.PreviousColumns(), frame.FailedColumns)
		if len(causes) == 0 {
			causes = append(causes, cause{kind: causeOther, text: "no_row"})
		}
	}
	if len(causes) == 0 && hasRow {
		if _, valid := scaleOf(rule, id, row); !valid {
			causes = append(causes, cause{kind: causeOther, text: reasonScaleInvalid})
		}
	}
	return causes
}

// reasonMissingKept 是写了 on_missing 的规则里，持有标的本根缺数、连续缺数没超过 keep_bars 而冻结保持的明细原因。
const reasonMissingKept = "missing_kept"

// frozenSetOnMissing 是策略里有规则写了 on_missing 时的冻结合并：
//   - 只有持有该标的的规则参与“要不要移出”的判断；没持有它的规则只把它当作缺数的候选，其他原因（缺数）不阻止冻结；
//   - 持有它的规则里出现其他原因（本根缺数）时：写了 on_missing 且连续缺数不超过 keep_bars 的，按缺数冻结保持（missing_kept）；
//     没写 on_missing、或已超过 keep_bars 的，阻止冻结，按现状移出；
//   - 市场不可用、只缺上一根的冻结与现状相同（任一规则里出现都可以保护）。
func frozenSetOnMissing(program *dsl.Program, frame Frame, previous State, held heldIndex) (map[string]struct{}, map[string]string, map[string]map[string]int) {
	frozen, reasons := make(map[string]struct{}), make(map[string]string)
	streaks := make(map[string]map[string]int)
	for _, id := range held.subjects() {
		protectable, blocked, market, kept := false, false, false, false
		next := make(map[string]int)
		for i := range program.Rules {
			rule := &program.Rules[i]
			ruleID := rule.Rule.ID
			if !contains(frame.Expected[ruleID], id) {
				continue
			}
			ruleHeld := contains(previous.Rules[ruleID].Held, id)
			if !ruleHeld && contains(frame.AgedOut[ruleID], id) {
				continue
			}
			if _, noRow := frame.shortNoRow[ruleID][id]; noRow {
				blocked = true // 永续已经没有它的空头（已下架）：不冻结，与没写 on_missing 时相同
				continue
			}
			other := false
			for _, c := range ruleCauses(rule, frame, id, ruleHeld) {
				switch c.kind {
				case causeOther:
					other = true
				case causeMarket:
					protectable, market = true, true
				default:
					protectable = true
				}
			}
			if !other || !ruleHeld {
				continue
			}
			streak := previous.Rules[ruleID].MissingStreak[id] + 1
			if rule.Rule.OnMissing == nil || streak > rule.Rule.OnMissing.KeepBars {
				blocked = true
				continue
			}
			protectable, kept = true, true
			next[ruleID] = streak
		}
		if !protectable || blocked {
			continue
		}
		frozen[id] = struct{}{}
		switch {
		case market:
			reasons[id] = reasonMarketHold
		case kept:
			reasons[id] = reasonMissingKept
		default:
			reasons[id] = reasonPrevHold
		}
		for ruleID, streak := range next {
			if streaks[ruleID] == nil {
				streaks[ruleID] = make(map[string]int)
			}
			streaks[ruleID][id] = streak
		}
	}
	return frozen, reasons, streaks
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
