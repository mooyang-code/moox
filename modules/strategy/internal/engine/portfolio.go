package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// applyPortfolio 把各规则的权重相加（做空为负）并执行组合级约束：
// 单标的上限超出留现金、gross 不超过 leverage、现货不允许负权重且 Σw ≤ 1，最后写入目标、现金与换手。
// frozen 是因 K 线空洞缺数而保持持仓的标的：它们的目标是保持标记，权重取上期组合目标（预留的预算占用），
// 其余标的在剩余预算内按比例缩小（见 reserveFrozen）。
func applyPortfolio(decision *Decision, portfolio dsl.Portfolio, spot bool, contributions map[string]quant.Decimal, frozen map[string]struct{}, previous State, tolerance quant.Decimal) error {
	for id := range frozen {
		delete(contributions, id) // 规则不给冻结标的配权，保险起见再删一次
	}
	quantized := quantizeTargets(contributions, portfolio, spot, tolerance)
	for _, note := range quantized.notes {
		decision.Summary.Notes = append(decision.Summary.Notes, note)
	}
	quantized = reserveFrozen(quantized, frozen, previous, portfolio, spot, &decision.Summary)
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

// reserveFrozen 给保持持仓的标的预留预算并追加保持目标：预留 R = Σ|上期目标权重|，预算上限 B 取 portfolio.leverage
// （现货不超过 1）；非冻结标的的总敞口 G 超过 B − R 时整体乘以 (B − R) / G 后向零截断，只缩小不放大；R ≥ B 时其余目标为零。
// 保持目标的权重取上期值并写回 State.Targets，下一期仍能读到；Trade 对保持目标不换算数量，也不调仓。
func reserveFrozen(quantized quantizedTargets, frozen map[string]struct{}, previous State, portfolio dsl.Portfolio, spot bool, summary *Summary) quantizedTargets {
	if len(frozen) == 0 {
		return quantized
	}
	ids := make([]string, 0, len(frozen))
	for id := range frozen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	reserved := quant.Zero()
	holds := make([]Target, 0, len(ids))
	for _, id := range ids {
		weight, err := quant.Parse(previous.Targets[id])
		if err != nil || weight.IsZero() {
			continue
		}
		reserved = reserved.Add(abs(weight))
		holds = append(holds, Target{InstrumentID: id, Weight: weight, Hold: true})
	}
	budget := portfolio.Leverage
	if spot && budget.Cmp(quant.One()) > 0 {
		budget = quant.One()
	}
	remaining := budget.Sub(reserved)
	if remaining.IsNegative() {
		remaining = quant.Zero()
	}
	if quantized.gross.Cmp(remaining) > 0 {
		scaled := quantizedTargets{gross: quant.Zero(), net: quant.Zero(), notes: quantized.notes}
		for _, target := range quantized.targets {
			weight := quant.Zero()
			if !remaining.IsZero() {
				weight = target.Weight.Mul(remaining).Div(quantized.gross).TruncateTo(WeightPlaces)
			}
			if weight.IsZero() {
				continue
			}
			scaled.gross = scaled.gross.Add(abs(weight))
			scaled.net = scaled.net.Add(weight)
			scaled.targets = append(scaled.targets, Target{InstrumentID: target.InstrumentID, Weight: weight})
		}
		summary.Notes = append(summary.Notes, fmt.Sprintf("保持持仓预留 %s，其余目标总敞口 %s 超过剩余预算 %s，按比例缩小为 %s", reserved.String(), quantized.gross.String(), remaining.String(), scaled.gross.String()))
		quantized = scaled
	}
	for _, hold := range holds {
		quantized.gross = quantized.gross.Add(abs(hold.Weight))
		quantized.net = quantized.net.Add(hold.Weight)
	}
	quantized.targets = append(quantized.targets, holds...)
	sort.Slice(quantized.targets, func(i, j int) bool { return quantized.targets[i].InstrumentID < quantized.targets[j].InstrumentID })
	held := make([]string, 0, len(holds))
	for _, hold := range holds {
		held = append(held, hold.InstrumentID)
	}
	summary.Notes = append(summary.Notes, fmt.Sprintf("%d 个标的因数据空洞保持持仓：%s", len(held), strings.Join(held, "、")))
	return quantized
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
