package engine

import (
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// Evaluate 对一帧输入求值。只有编程错误（空程序）返回 error；配置或数据问题以 skipped 决策表达。
// 流程：按规则划分集合并守门 → 逐规则求值 → 组合合成与约束 → 解释明细排序。
func Evaluate(program *dsl.Program, frame Frame, previous State) (Decision, error) {
	if program == nil {
		return Decision{}, errors.New("策略程序为空")
	}
	mode := program.Strategy.Market
	if mode.IsZero() {
		return Decision{}, errors.New("策略程序没有确定市场模式")
	}
	held := indexHeld(previous)
	// 没写规则级市场的规则看的持仓：去掉只被写了规则级市场的规则持有的标的（见 rulemarket.go；没有这类规则时就是 held）。
	poolHeld := unmarkedHeld(program, mode, previous, held)
	columns := append(append([]string(nil), program.Columns...), program.PreviousColumns...)
	views := ruleMarketViews(program, mode, frame, previous)
	frame = restrictShortCandidates(program, mode, selectPool(mode, frame, poolHeld, columns), previous, poolHeld)
	decision := evaluateFrame(program, mode, frame, views, previous, held, poolHeld)
	labelItems(program, mode, frame, poolHeld, decision.Items)
	sortItems(program, decision.Items)
	return decision, nil
}

func evaluateFrame(program *dsl.Program, mode dsl.Market, frame Frame, views map[string]Frame, previous State, held, poolHeld heldIndex) Decision {
	decision := Decision{
		Status:  StatusOK,
		State:   State{Rules: make(map[string]RuleState, len(program.Rules)), Targets: map[string]string{}},
		Summary: Summary{Universe: len(frame.Universe), Rules: make(map[string]RuleSummary, len(program.Rules))},
	}
	// 因所在市场本期不可用（或只缺上一根）缺数的已持仓标的在所有规则里冻结：不求值、不调仓，持仓数量保持不动（见 freeze.go）。
	frozen, holdReasons, streaks := frozenSet(program, frame, previous, poolHeld.subjects(), "")
	// 写了规则级市场的规则按（标的, 市场）键冻结：只在写了同一市场的规则之间、用该市场的视图判断（见 rulemarket.go）。
	// marketFrozen 是这些规则划分集合用的冻结集合（按币冻结的标的仍对它们冻结），frozenKeys 交给组合层按键保持。
	marketFrozen, marketReasons := map[string]map[string]struct{}{}, map[string]map[string]string{}
	frozenKeys := map[string]struct{}{}
	for market, view := range views {
		keyed, reasons, keyedStreaks := frozenSet(program, view, previous, held.subjectsIn(market), market)
		merged, mergedReasons := make(map[string]struct{}, len(frozen)+len(keyed)), make(map[string]string, len(holdReasons)+len(reasons))
		for id := range keyed {
			merged[id], mergedReasons[id] = struct{}{}, reasons[id]
			frozenKeys[TargetKey(id, market)] = struct{}{}
		}
		for id := range frozen {
			merged[id], mergedReasons[id] = struct{}{}, holdReasons[id]
		}
		marketFrozen[market], marketReasons[market] = merged, mergedReasons
		for ruleID, streak := range keyedStreaks {
			if streaks == nil {
				streaks = make(map[string]map[string]int)
			}
			streaks[ruleID] = streak
		}
	}
	// 第一遍：集合划分与守门。守门失败也要给出全部规则的集合摘要与缺数明细，便于排查。
	partitions := make([]ruleSets, len(program.Rules))
	skipReason, skipNote := "", ""
	if len(frame.Universe) == 0 {
		skipReason, skipNote = SkipNoData, "本期基础集合为空：没有任何标的的数据"
	}
	for i := range program.Rules {
		rule := &program.Rules[i]
		ruleMarket := dsl.RuleMarket(mode, rule.Rule)
		ruleFrame := frameFor(mode, rule.Rule, frame, views)
		var sets ruleSets
		switch {
		case rule.Rule.Type == dsl.RuleTypeTranche:
			sets = partitionTranche(rule, ruleFrame, previous.Rules[rule.Rule.ID].Held)
		case ruleMarket != "":
			sets = partitionRule(rule, ruleFrame, previous.Rules[rule.Rule.ID].Held, marketFrozen[ruleMarket], marketReasons[ruleMarket])
		default:
			sets = partitionRule(rule, ruleFrame, previous.Rules[rule.Rule.ID].Held, frozen, holdReasons)
		}
		if ruleMarket != "" {
			// 规则所在市场本期整体不可用：视图里没有另一个市场的行可借，候选都带着 market_unavailable，守门必然整期跳过（设计 3.2.1：
			// 只有一部分市场不可用时不整期跳过）。这条规则本期只保持冻结的键，不新开仓。
			sets.waived = marketUnavailable(frame, ruleMarket)
		} else {
			sets.waived = waivesShortGuard(mode, rule.Rule, frame, sets)
		}
		partitions[i] = sets
		decision.Summary.Rules[rule.Rule.ID] = RuleSummary{
			Expected:  len(sets.expected),
			AgedOut:   len(sets.agedOut),
			Available: len(sets.available),
			Missing:   len(sets.missing),
			Held:      len(sets.frozen),
			Budget:    rule.Rule.Weight.Total.String(),
		}
		decision.Items = append(decision.Items, sets.items(rule.Rule.ID)...)
		if skipReason != "" {
			continue
		}
		if sets.waived && ruleMarket != "" {
			decision.Summary.Notes = append(decision.Summary.Notes, fmt.Sprintf("%s市场本期整体不可用，规则 %s（market: %s）不守门：冻结的持仓保持不动，不新开仓", ruleMarket, rule.Rule.ID, ruleMarket))
			continue
		}
		if sets.waived {
			decision.Summary.Notes = append(decision.Summary.Notes, fmt.Sprintf("永续本期整体不可用，空头规则 %s 不守门：冻结的空头保持不动，不新开空头", rule.Rule.ID))
			continue
		}
		// allow_empty 的规则不参与任何整期守门：候选为空或缺数时这条规则本期只是不出目标，预算留现金。
		if rule.Rule.AllowEmpty {
			continue
		}
		if reason := guard(program.Strategy.Portfolio, rule.Rule.Type, sets, guardedFrozen(mode, sets, previous.Rules[rule.Rule.ID].Held)); reason != "" {
			skipReason = reason
			skipNote = fmt.Sprintf("规则 %s：预期 %d、年龄剔除 %d、可用 %d、缺数 %d", rule.Rule.ID, len(sets.expected), len(sets.agedOut), len(sets.available), len(sets.missing))
		}
	}
	if skipReason != "" {
		for i := range program.Rules {
			decision.Items = append(decision.Items, partitions[i].unevaluated(program.Rules[i].Rule.ID)...)
		}
		return skipped(decision, previous, skipReason, skipNote)
	}
	// 第二遍：逐规则求值。
	tolerance := quantizationTolerance(program)
	contributions := make(map[string]quant.Decimal)
	// groups 是各规则本期的分组目标（键与 contributions 相同），只在写了 portfolio.limits 时收集，超限转移按它分配。
	var groups map[string]map[string]quant.Decimal
	if program.Strategy.Portfolio.Limits != nil {
		groups = make(map[string]map[string]quant.Decimal, len(program.Rules))
	}
	for i := range program.Rules {
		rule := &program.Rules[i]
		ruleMarket := dsl.RuleMarket(mode, rule.Rule)
		ruleFrame := frameFor(mode, rule.Rule, frame, views)
		var result ruleResult
		var err error
		switch rule.Rule.Type {
		case dsl.RuleTypeRank:
			result, err = evaluateRank(rule, ruleFrame, partitions[i], previous.Rules[rule.Rule.ID], tolerance)
		case dsl.RuleTypeSignal:
			result, err = evaluateSignal(rule, ruleFrame, partitions[i], previous.Rules[rule.Rule.ID], tolerance)
		case dsl.RuleTypeTranche:
			result, err = evaluateTranche(rule, ruleFrame, partitions[i], previous.Rules[rule.Rule.ID], tolerance)
		default:
			err = fmt.Errorf("类型 %q 不受支持", rule.Rule.Type)
		}
		if err != nil {
			// 出错的规则与排在它后面的规则都还没有产出明细：补齐可用标的，使每条规则的解释仍覆盖全部 E(r)。
			for j := i; j < len(program.Rules); j++ {
				decision.Items = append(decision.Items, partitions[j].unevaluated(program.Rules[j].Rule.ID)...)
			}
			return skipped(decision, previous, SkipConfigError, fmt.Sprintf("规则 %s：%v", rule.Rule.ID, err))
		}
		applyScale(rule, ruleFrame, &result, tolerance)
		if streak := streaks[rule.Rule.ID]; len(streak) > 0 {
			result.state.MissingStreak = streak
		}
		allocated := quant.Zero()
		var group map[string]quant.Decimal
		if groups != nil {
			group = make(map[string]quant.Decimal, len(result.weights))
			groups[rule.Rule.ID] = group
		}
		for id, weight := range result.weights {
			market := ruleMarket
			if market == "" {
				market = contributionMarket(mode, frame, poolHeld, id, weight.IsNegative())
			}
			key := TargetKey(id, market)
			contributions[key] = contributions[key].Add(weight)
			if group != nil {
				group[key] = group[key].Add(weight)
			}
			allocated = allocated.Add(abs(truncateWeightWith(weight, tolerance)))
		}
		summary := decision.Summary.Rules[rule.Rule.ID]
		summary.Filtered, summary.Scored, summary.Selected, summary.Weighted = result.filtered, result.scored, result.selected, len(result.weights)
		summary.Allocated = allocated.String()
		summary.StopLoss, summary.TrailingStop, summary.TakeProfit = result.stops[reasonStopLoss], result.stops[reasonTrailingStop], result.stops[reasonTakeProfit]
		decision.Summary.Rules[rule.Rule.ID] = summary
		decision.State.Rules[rule.Rule.ID] = result.state
		decision.Summary.Notes = append(decision.Summary.Notes, result.notes...)
		decision.Items = mergeStopItems(decision.Items, result.items)
		decision.Items = append(decision.Items, partitions[i].notExpected(rule.Rule.ID, previous.Rules[rule.Rule.ID].Held)...)
	}
	if err := applyPortfolio(&decision, program.Strategy.Portfolio, mode, contributions, groups, frozen, frozenKeys, previous, held, tolerance); err != nil {
		return skipped(decision, previous, SkipConfigError, err.Error())
	}
	return decision
}

// mergeStopItems 追加规则的求值明细。止损的标的可能已经有集合划分给出的明细（例如 signal 持仓的 exit 因子缺失、
// 但 close 已达止损线），hold 规则缺数出场的持仓也已经有缺数明细：这时改写那一条，原因附上原有原因，保证每个（规则, 标的）只有一条。
func mergeStopItems(items, ruleItems []Item) []Item {
	var index map[string]int
	for _, item := range ruleItems {
		if item.Stage == StageStopped || item.Reason == reasonExitMissing {
			if index == nil {
				index = make(map[string]int, len(items))
				for i, existing := range items {
					index[existing.RuleID+"\x00"+existing.InstrumentID] = i
				}
			}
			if at, ok := index[item.RuleID+"\x00"+item.InstrumentID]; ok {
				if items[at].Reason != "" {
					item.Reason += ";" + items[at].Reason
				}
				items[at] = item
				continue
			}
		}
		items = append(items, item)
	}
	return items
}

// skipped 把决策改为跳过：无目标、状态沿用前序，并追加说明。
func skipped(decision Decision, previous State, reason, note string) Decision {
	decision.Status = StatusSkipped
	decision.SkipReason = reason
	decision.Targets = nil
	decision.State = previous
	if note != "" {
		decision.Summary.Notes = append(decision.Summary.Notes, note)
	}
	return decision
}

// guard 是规则的守门，任一规则不通过都整期跳过，空候选与大面积缺数不能当作"选不出标的"或"退出"去清仓：
// rank 规则没有可评估的候选（预期集合为空或全部年龄剔除）、进入 filter 的可用标的少于 min_universe 时
// 记 universe_too_small；rank 与 signal 规则的缺数占（预期 − 年龄剔除）的比例超过 max_missing 时记
// too_many_missing。signal 规则的池是显式的，不受 min_universe 约束，池为空时不守门。frozen 是计入缺数的冻结标的数（见 guardedFrozen）。
func guard(portfolio dsl.Portfolio, ruleType string, sets ruleSets, frozen int) string {
	denominator := len(sets.expected) - len(sets.agedOut)
	if (ruleType == dsl.RuleTypeRank || ruleType == dsl.RuleTypeTranche) && (denominator <= 0 || len(sets.available) < portfolio.MinUniverse) {
		return SkipUniverseTooSmall
	}
	// 冻结（保持持仓）的标的仍计入缺数比例：保护中的标的不能伪装成可用。
	missing := len(sets.missing) + frozen
	if denominator > 0 && missing > 0 {
		ratio := quant.Must(fmt.Sprint(missing)).Div(quant.Must(fmt.Sprint(denominator)))
		if ratio.Cmp(portfolio.MaxMissing) > 0 {
			return SkipTooManyMissing
		}
	}
	return ""
}

// guardedFrozen 返回规则守门计入缺数的冻结标的数。通常是规则预期集合里的全部冻结标的；多市场模式下只排除一种：本规则上期没持有、
// 并且是因为所在市场本期整体不可用而冻结（market_hold）的标的。冻结按币，别的规则在不可用市场的持仓（例如永续不可用时的永续空头）
// 会把这个币在所有规则里冻结，本规则没持有它，它对本规则只是本期不参与的候选，计进缺数会让一部分市场不可用变成整期跳过（设计 3.2.1）。
// 本规则持有的冻结标的、以及只缺上一根而冻结（prev_hold）的标的照旧计入：只缺上一根与另一个市场可不可用无关。
func guardedFrozen(mode dsl.Market, sets ruleSets, ruleHeld []string) int {
	if mode.Single() {
		return len(sets.frozen)
	}
	count := 0
	for id := range sets.frozen {
		if contains(ruleHeld, id) || sets.holdReason[id] != reasonMarketHold {
			count++
		}
	}
	return count
}
