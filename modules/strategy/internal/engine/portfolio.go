package engine

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// applyPortfolio 把各规则的权重相加（做空为负）并执行组合级约束：
// 单标的上限超出留现金、gross 不超过 leverage、现货不允许负权重且 Σw ≤ 1，最后写入目标、现金与换手。
func applyPortfolio(decision *Decision, portfolio dsl.Portfolio, spot bool, contributions map[string]quant.Decimal, previous State) error {
	gross, net := quant.Zero(), quant.Zero()
	targets := make([]Target, 0, len(contributions))
	for _, id := range sortedIDs(contributions) {
		weight := truncateWeight(contributions[id])
		if weight.IsZero() {
			continue
		}
		if portfolio.HasMaxWeight && abs(weight).Cmp(portfolio.MaxWeight) > 0 {
			capped := portfolio.MaxWeight
			if weight.IsNegative() {
				capped = capped.Neg()
			}
			decision.Summary.Notes = append(decision.Summary.Notes, fmt.Sprintf("%s 的合成权重 %s 超过 portfolio.max_weight，裁剪为 %s，超出部分留现金", id, weight.String(), capped.String()))
			weight = truncateWeight(capped)
		}
		if spot && weight.IsNegative() {
			return fmt.Errorf("现货 View 不允许负权重：%s = %s", id, weight.String())
		}
		gross = gross.Add(abs(weight))
		net = net.Add(weight)
		targets = append(targets, Target{InstrumentID: id, Weight: weight})
	}
	if gross.Cmp(portfolio.Leverage) > 0 {
		return fmt.Errorf("合成后的 gross %s 超过 portfolio.leverage %s", gross.String(), portfolio.Leverage.String())
	}
	if spot && net.Cmp(quant.One()) > 0 {
		return fmt.Errorf("现货 View 的权重之和 %s 超过 1", net.String())
	}
	decision.Targets = targets
	decision.Summary.Gross = gross.String()
	decision.Summary.Net = net.String()
	decision.Summary.Cash = quant.One().Sub(net).String()
	decision.Summary.Turnover = turnover(targets, previous.Targets).RoundTo(WeightPlaces).String()
	for _, target := range targets {
		decision.State.Targets[target.InstrumentID] = target.Weight.String()
	}
	return nil
}

// turnover 是与上期理论目标相比的单边换手：Σ|w_new − w_old| / 2。
func turnover(targets []Target, previous map[string]string) quant.Decimal {
	current := make(map[string]quant.Decimal, len(targets))
	for _, target := range targets {
		current[target.InstrumentID] = target.Weight
	}
	ids := make(map[string]struct{}, len(current)+len(previous))
	for id := range current {
		ids[id] = struct{}{}
	}
	for id := range previous {
		ids[id] = struct{}{}
	}
	total := quant.Zero()
	for id := range ids {
		old := quant.Zero()
		if raw, ok := previous[id]; ok {
			if parsed, err := quant.Parse(raw); err == nil {
				old = parsed
			}
		}
		total = total.Add(abs(current[id].Sub(old)))
	}
	return total.Div(quant.Must("2"))
}

func abs(value quant.Decimal) quant.Decimal {
	if value.IsNegative() {
		return value.Neg()
	}
	return value
}

func sortedIDs(values map[string]quant.Decimal) []string {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
