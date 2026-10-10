package engine

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// applyPortfolio 把各规则的权重相加（做空为负）并执行组合级约束：
// 单标的上限超出留现金、gross 不超过 leverage、现货不允许负权重且 Σw ≤ 1，最后写入目标、现金与换手。
func applyPortfolio(decision *Decision, portfolio dsl.Portfolio, spot bool, contributions map[string]quant.Decimal, previous State, tolerance quant.Decimal) error {
	quantized := quantizeTargets(contributions, portfolio, spot, tolerance)
	for _, note := range quantized.notes {
		decision.Summary.Notes = append(decision.Summary.Notes, note)
	}
	for _, target := range quantized.targets {
		if spot && target.Weight.IsNegative() {
			return fmt.Errorf("现货 View 不允许负权重：%s = %s", target.InstrumentID, target.Weight.String())
		}
	}
	if quantized.gross.Cmp(portfolio.Leverage) > 0 {
		return fmt.Errorf("合成后的 gross %s 超过 portfolio.leverage %s", quantized.gross.String(), portfolio.Leverage.String())
	}
	if spot && quantized.net.Cmp(quant.One()) > 0 {
		return fmt.Errorf("现货 View 的权重之和 %s 超过 1", quantized.net.String())
	}
	decision.Targets = quantized.targets
	decision.Summary.Gross = quantized.gross.String()
	decision.Summary.Net = quantized.net.String()
	decision.Summary.Cash = quant.One().Sub(quantized.net).String()
	decision.Summary.Turnover = turnover(quantized.targets, previous.Targets).RoundTo(WeightPlaces).String()
	for _, target := range quantized.targets {
		decision.State.Targets[target.InstrumentID] = target.Weight.String()
	}
	return nil
}

// quantizedTargets 是一次量化的结果：max_weight 裁剪、向零截断后仍非零的目标，以及它们的 gross、net。
type quantizedTargets struct {
	targets    []Target
	gross, net quant.Decimal
	notes      []string
}

// quantizeTargets 对每个标的的合成权重先套 max_weight 再向零截断（noiseTolerance 为 0 时是精确截断）；裁剪或截断后为零的
// 标的不再出现在目标里（零权重的目标项会让下游为它读报价，空目标才能直接转现金）。
func quantizeTargets(contributions map[string]quant.Decimal, portfolio dsl.Portfolio, spot bool, noiseTolerance quant.Decimal) quantizedTargets {
	result := quantizedTargets{gross: quant.Zero(), net: quant.Zero(), targets: make([]Target, 0, len(contributions))}
	for _, id := range sortedIDs(contributions) {
		weight := contributions[id]
		if weight.IsZero() {
			continue
		}
		if portfolio.HasMaxWeight && abs(weight).Cmp(portfolio.MaxWeight) > 0 {
			capped := portfolio.MaxWeight
			if weight.IsNegative() {
				capped = capped.Neg()
			}
			result.notes = append(result.notes, fmt.Sprintf("%s 的合成权重 %s 超过 portfolio.max_weight，裁剪为 %s，超出部分留现金", id, weight.String(), capped.String()))
			weight = capped
		}
		weight = truncateWeightWith(weight, noiseTolerance)
		if weight.IsZero() {
			continue
		}
		result.gross = result.gross.Add(abs(weight))
		result.net = result.net.Add(weight)
		result.targets = append(result.targets, Target{InstrumentID: id, Weight: weight})
	}
	return result
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
