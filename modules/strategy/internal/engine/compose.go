package engine

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// ComposeTargets 把已按（标的|市场）合成的权重套上组合约束（max_weight 截断、保持目标预留预算、现货预算、leverage 缩小，见 applyPortfolio），
// 返回 ok 决策：目标、摘要（gross、net、换手与说明）与只含目标的 State。策略池用它把“比例 × 成员目标”的合成结果交给与普通策略同一套约束。
// mode 是池的有效市场模式（成员市场的并集）；frozen 是需要保持持仓的标的（成员因市场不可用或只缺上一根冻结的并集），保持上期池目标的权重。
func ComposeTargets(portfolio dsl.Portfolio, mode dsl.Market, contributions map[string]quant.Decimal, frozen map[string]struct{}, previous State) (Decision, error) {
	decision := Decision{
		Status:  StatusOK,
		State:   State{Targets: map[string]string{}},
		Summary: Summary{Rules: map[string]RuleSummary{}},
	}
	merged := make(map[string]quant.Decimal, len(contributions))
	for key, weight := range contributions {
		merged[key] = weight
	}
	if err := applyPortfolio(&decision, portfolio, mode, merged, frozen, nil, previous, indexHeld(previous), poolTolerance(portfolio)); err != nil {
		return Decision{}, err
	}
	return decision, nil
}

// poolTolerance 与 quantizationTolerance 同理：组合的上限都落在输出网格上时才容忍定点噪声（比例 1/3 这类份额参与乘法留下的尾差）。
func poolTolerance(portfolio dsl.Portfolio) quant.Decimal {
	onGrid := func(value quant.Decimal) bool { return value.TruncateTo(WeightPlaces).Cmp(value) == 0 }
	if !onGrid(portfolio.Leverage) || (portfolio.HasMaxWeight && !onGrid(portfolio.MaxWeight)) {
		return quant.Zero()
	}
	return noise
}

// HeldSubjects 返回成员目标里带保持标记的标的（成员因市场不可用或只缺上一根冻结的标的）。
func HeldSubjects(targets []Target) []string {
	var ids []string
	for _, target := range targets {
		if target.Hold {
			ids = append(ids, target.InstrumentID)
		}
	}
	return ids
}
