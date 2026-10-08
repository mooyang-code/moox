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
	decision := evaluateFrame(program, frame, previous)
	sortItems(program, decision.Items)
	return decision, nil
}

func evaluateFrame(program *dsl.Program, frame Frame, previous State) Decision {
	decision := Decision{
		Status:  StatusOK,
		State:   State{Rules: make(map[string]RuleState, len(program.Rules)), Targets: map[string]string{}},
		Summary: Summary{Universe: len(frame.Universe), Rules: make(map[string]RuleSummary, len(program.Rules))},
	}
	// 第一遍：集合划分与守门。守门失败也要给出全部规则的集合摘要与缺数明细，便于排查。
	partitions := make([]ruleSets, len(program.Rules))
	skipReason, skipNote := "", ""
	if len(frame.Universe) == 0 {
		skipReason, skipNote = SkipNoData, "本期基础集合为空：没有任何标的的数据"
	}
	for i := range program.Rules {
		rule := &program.Rules[i]
		sets := partitionRule(rule, frame)
		partitions[i] = sets
		decision.Summary.Rules[rule.Rule.ID] = RuleSummary{
			Expected:  len(sets.expected),
			AgedOut:   len(sets.agedOut),
			Available: len(sets.available),
			Missing:   len(sets.missing),
			Budget:    rule.Rule.Weight.Total.String(),
		}
		decision.Items = append(decision.Items, sets.items(rule.Rule.ID)...)
		if skipReason != "" || rule.Rule.Type != dsl.RuleTypeRank {
			continue
		}
		if reason := guard(program.Strategy.Portfolio, sets); reason != "" {
			skipReason = reason
			skipNote = fmt.Sprintf("规则 %s：预期 %d、年龄剔除 %d、可用 %d、缺数 %d", rule.Rule.ID, len(sets.expected), len(sets.agedOut), len(sets.available), len(sets.missing))
		}
	}
	if skipReason != "" {
		return skipped(decision, previous, skipReason, skipNote)
	}
	// 第二遍：逐规则求值。
	contributions := make(map[string]quant.Decimal)
	for i := range program.Rules {
		rule := &program.Rules[i]
		var result ruleResult
		var err error
		switch rule.Rule.Type {
		case dsl.RuleTypeRank:
			result, err = evaluateRank(rule, frame, partitions[i], previous.Rules[rule.Rule.ID])
		case dsl.RuleTypeSignal:
			result, err = evaluateSignal(rule, frame, partitions[i], previous.Rules[rule.Rule.ID])
		default:
			err = fmt.Errorf("类型 %q 不受支持", rule.Rule.Type)
		}
		if err != nil {
			return skipped(decision, previous, SkipConfigError, fmt.Sprintf("规则 %s：%v", rule.Rule.ID, err))
		}
		allocated := quant.Zero()
		for id, weight := range result.weights {
			contributions[id] = contributions[id].Add(weight)
			allocated = allocated.Add(abs(weight))
		}
		summary := decision.Summary.Rules[rule.Rule.ID]
		summary.Filtered, summary.Scored, summary.Selected, summary.Weighted = result.filtered, result.scored, result.selected, len(result.weights)
		summary.Allocated = allocated.String()
		decision.Summary.Rules[rule.Rule.ID] = summary
		decision.State.Rules[rule.Rule.ID] = result.state
		decision.Items = append(decision.Items, result.items...)
		decision.Items = append(decision.Items, partitions[i].notExpected(rule.Rule.ID, previous.Rules[rule.Rule.ID].Held)...)
	}
	if err := applyPortfolio(&decision, program.Strategy.Portfolio, frame.Spot, contributions, previous); err != nil {
		return skipped(decision, previous, SkipConfigError, err.Error())
	}
	return decision
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

// guard 是 rank 规则的守门：没有可评估的候选（预期集合为空或全部年龄剔除）、进入 filter 的可用标的太少，
// 或缺数占（预期 − 年龄剔除）的比例过高，都整期跳过。空候选不能当作"选不出标的"去清仓。
func guard(portfolio dsl.Portfolio, sets ruleSets) string {
	denominator := len(sets.expected) - len(sets.agedOut)
	if denominator <= 0 || len(sets.available) < portfolio.MinUniverse {
		return SkipUniverseTooSmall
	}
	if len(sets.missing) > 0 {
		ratio := quant.Must(fmt.Sprint(len(sets.missing))).Div(quant.Must(fmt.Sprint(denominator)))
		if ratio.Cmp(portfolio.MaxMissing) > 0 {
			return SkipTooManyMissing
		}
	}
	return ""
}
