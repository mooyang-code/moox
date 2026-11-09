package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// applyPortfolio 把各规则的权重按（标的, 市场）合成并执行组合级约束，固定顺序（设计 3.1“组合约束”）：
//  1. max_weight 截断，按币合计：同一个标的现货、永续权重绝对值之和不超过它；
//  2. 给保持目标预留预算（保持目标本身不缩小）；
//  3. 现货部分超过“1 − 现货保持目标”时按比例缩小，多出的部分留作现金（只有 leverage 大于 1 时才可能超过）；
//  4. gross 超过 portfolio.leverage 时按比例缩小；
//  5. 写入目标、摘要与换手。
//
// 现货负权重仍然报错：它说明策略或定市场的逻辑有错。frozen 是因市场不可用或只缺上一根而保持持仓的标的：
// 按标的展开成上期所有非零的（标的, 市场）键，它们的目标是保持标记，权重取上期值。frozenKeys 是按键冻结的（标的, 市场）键
// （写了规则级市场的规则，只冻结那个市场的键，见 rulemarket.go），同样保持上期值。
func applyPortfolio(decision *Decision, portfolio dsl.Portfolio, mode dsl.Market, contributions map[string]quant.Decimal, frozen, frozenKeys map[string]struct{}, previous State, held heldIndex, tolerance quant.Decimal) error {
	for key := range contributions {
		if id, _ := SplitTargetKey(key); contains2(frozen, id) || contains2(frozenKeys, key) {
			delete(contributions, key) // 规则不给冻结标的配权，保险起见再删一次
		}
	}
	quantized := quantizeTargets(contributions, portfolio, tolerance)
	decision.Summary.Notes = append(decision.Summary.Notes, quantized.notes...)
	holds := holdTargets(frozen, frozenKeys, previous, held)
	quantized = fitBudget(quantized, holds, portfolio, mode, &decision.Summary)
	for _, target := range quantized.targets {
		if dsl.SpotLike(target.Market) && target.Weight.IsNegative() {
			return fmt.Errorf("现货不允许负权重：%s = %s", target.InstrumentID, target.Weight.String())
		}
	}
	decision.Targets = quantized.targets
	decision.Summary.Gross = quantized.gross.String()
	decision.Summary.Net = quantized.net.String()
	if mode.Single() {
		decision.Summary.Cash = quant.One().Sub(quantized.net).String()
	} else {
		spotNet, swapNet := quant.Zero(), quant.Zero()
		for _, target := range quantized.targets {
			if target.Market == dsl.MarketSwap {
				swapNet = swapNet.Add(target.Weight)
			} else {
				spotNet = spotNet.Add(target.Weight)
			}
		}
		decision.Summary.SpotNet, decision.Summary.SwapNet = spotNet.String(), swapNet.String()
	}
	decision.Summary.Turnover = turnover(quantized.targets, previous.Targets).RoundTo(WeightPlaces).String()
	for _, target := range quantized.targets {
		decision.State.Targets[TargetKey(target.InstrumentID, target.Market)] = target.Weight.String()
	}
	return nil
}

func contains2(set map[string]struct{}, id string) bool {
	_, ok := set[id]
	return ok
}

// holdTargets 返回冻结标的的保持目标：按币冻结的标的展开成上期所有非零键，按键冻结的只取那个键，权重取上期值（按键排序）。
func holdTargets(frozen, frozenKeys map[string]struct{}, previous State, held heldIndex) []Target {
	keys := make(map[string]struct{}, len(frozen)+len(frozenKeys))
	for id := range frozen {
		for _, key := range held.keys(id) {
			keys[key] = struct{}{}
		}
	}
	for key := range frozenKeys {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sortKeys(ordered)
	var holds []Target
	for _, key := range ordered {
		weight, err := quant.Parse(previous.Targets[key])
		if err != nil || weight.IsZero() {
			continue
		}
		id, market := SplitTargetKey(key)
		holds = append(holds, Target{InstrumentID: id, Market: market, Weight: weight, Hold: true})
	}
	return holds
}

// fitBudget 给保持目标预留预算并把其余目标缩进预算（组合约束第 2～4 步），追加保持目标并按（标的, 市场）排序。
//
// 预留 R = Σ|保持目标权重|，总预算是 portfolio.leverage。
//   - 现货预算不超过 1：非保持的现货部分超过“1 − 现货保持目标”时，现货目标整体按比例缩小（leverage 不大于 1 时
//     这一步被 gross 约束包含，不单独执行）；
//   - 非保持目标的总敞口 G 超过 leverage − R 时整体乘以 (leverage − R) / G 后向零截断，只缩小不放大；R ≥ leverage 时其余目标为零。
func fitBudget(quantized quantizedTargets, holds []Target, portfolio dsl.Portfolio, mode dsl.Market, summary *Summary) quantizedTargets {
	reserved, spotReserved := quant.Zero(), quant.Zero()
	for _, hold := range holds {
		reserved = reserved.Add(abs(hold.Weight))
		if dsl.SpotLike(hold.Market) {
			spotReserved = spotReserved.Add(abs(hold.Weight))
		}
	}
	if portfolio.Leverage.Cmp(quant.One()) > 0 && mode.CanOutputSpotLike() {
		spotSum := quant.Zero()
		for _, target := range quantized.targets {
			if dsl.SpotLike(target.Market) && !target.Weight.IsNegative() {
				spotSum = spotSum.Add(target.Weight)
			}
		}
		spotRemaining := quant.One().Sub(spotReserved)
		if spotRemaining.IsNegative() {
			spotRemaining = quant.Zero()
		}
		if spotSum.Cmp(spotRemaining) > 0 {
			scaled := quantizedTargets{gross: quant.Zero(), net: quant.Zero(), notes: quantized.notes}
			for _, target := range quantized.targets {
				weight := target.Weight
				if dsl.SpotLike(target.Market) {
					weight = scaleWeight(weight, spotRemaining, spotSum)
				}
				scaled.add(Target{InstrumentID: target.InstrumentID, Market: target.Market, Weight: weight})
			}
			summary.Notes = append(summary.Notes, fmt.Sprintf("现货部分总权重 %s 超过现货预算 %s，现货目标按比例缩小为 %s，多出的部分留作现金", spotSum.String(), spotRemaining.String(), scaled.spotSum().String()))
			quantized = scaled
		}
	}
	remaining := portfolio.Leverage.Sub(reserved)
	if remaining.IsNegative() {
		remaining = quant.Zero()
	}
	if quantized.gross.Cmp(remaining) > 0 {
		scaled := quantizedTargets{gross: quant.Zero(), net: quant.Zero(), notes: quantized.notes}
		for _, target := range quantized.targets {
			scaled.add(Target{InstrumentID: target.InstrumentID, Market: target.Market, Weight: scaleWeight(target.Weight, remaining, quantized.gross)})
		}
		if len(holds) > 0 {
			summary.Notes = append(summary.Notes, fmt.Sprintf("保持持仓预留 %s，其余目标总敞口 %s 超过剩余预算 %s，按比例缩小为 %s", reserved.String(), quantized.gross.String(), remaining.String(), scaled.gross.String()))
		} else {
			summary.Notes = append(summary.Notes, fmt.Sprintf("总敞口 %s 超过 portfolio.leverage %s，按比例缩小为 %s", quantized.gross.String(), portfolio.Leverage.String(), scaled.gross.String()))
		}
		quantized = scaled
	}
	if len(holds) == 0 {
		return quantized
	}
	for _, hold := range holds {
		quantized.gross = quantized.gross.Add(abs(hold.Weight))
		quantized.net = quantized.net.Add(hold.Weight)
	}
	quantized.targets = append(quantized.targets, holds...)
	sort.Slice(quantized.targets, func(i, j int) bool {
		return lessKey(TargetKey(quantized.targets[i].InstrumentID, quantized.targets[i].Market), TargetKey(quantized.targets[j].InstrumentID, quantized.targets[j].Market))
	})
	heldNames := make([]string, 0, len(holds))
	for _, hold := range holds {
		heldNames = append(heldNames, hold.InstrumentID)
	}
	summary.Notes = append(summary.Notes, fmt.Sprintf("%d 个标的因市场不可用或只缺上一根保持持仓：%s", len(holdIDs(holds)), strings.Join(holdIDs(holds), "、")))
	return quantized
}

// holdIDs 返回保持目标的标的（去重，保持顺序）。
func holdIDs(holds []Target) []string {
	var ids []string
	for _, hold := range holds {
		if len(ids) == 0 || ids[len(ids)-1] != hold.InstrumentID {
			ids = append(ids, hold.InstrumentID)
		}
	}
	return ids
}

// scaleWeight 返回 weight × remaining / total 向零截断到权重精度；remaining 为零时是零。
func scaleWeight(weight, remaining, total quant.Decimal) quant.Decimal {
	if remaining.IsZero() {
		return quant.Zero()
	}
	return weight.Mul(remaining).Div(total).TruncateTo(WeightPlaces)
}

// quantizedTargets 是一次量化的结果：max_weight 裁剪、向零截断后仍非零的目标，以及它们的 gross、net。
type quantizedTargets struct {
	targets    []Target
	gross, net quant.Decimal
	notes      []string
}

// add 追加一个目标并累计 gross、net；权重为零的目标不再出现（零权重的目标项会让下游为它读报价，空目标才能直接转现金）。
func (q *quantizedTargets) add(target Target) {
	if target.Weight.IsZero() {
		return
	}
	q.gross = q.gross.Add(abs(target.Weight))
	q.net = q.net.Add(target.Weight)
	q.targets = append(q.targets, target)
}

func (q quantizedTargets) spotSum() quant.Decimal {
	sum := quant.Zero()
	for _, target := range q.targets {
		if dsl.SpotLike(target.Market) {
			sum = sum.Add(target.Weight)
		}
	}
	return sum
}

// quantizeTargets 对每个币的合成权重先套 max_weight 再向零截断（noiseTolerance 为 0 时是精确截断）；裁剪或截断后为零的
// 目标不再出现在结果里。max_weight 按币合计：同一个标的现货与永续两边都不为零时，绝对值之和超过上限就按比例缩小两边；
// 只有一边时沿用单边的写法（直接取 ±上限）。
func quantizeTargets(contributions map[string]quant.Decimal, portfolio dsl.Portfolio, noiseTolerance quant.Decimal) quantizedTargets {
	result := quantizedTargets{gross: quant.Zero(), net: quant.Zero(), targets: make([]Target, 0, len(contributions))}
	keys := make([]string, 0, len(contributions))
	for key, weight := range contributions {
		if !weight.IsZero() {
			keys = append(keys, key)
		}
	}
	sortKeys(keys)
	for i := 0; i < len(keys); {
		id, _ := SplitTargetKey(keys[i])
		j := i
		for j < len(keys) {
			if other, _ := SplitTargetKey(keys[j]); other != id {
				break
			}
			j++
		}
		group := keys[i:j]
		i = j
		if portfolio.HasMaxWeight && len(group) > 1 {
			total := quant.Zero()
			for _, key := range group {
				total = total.Add(abs(contributions[key]))
			}
			if total.Cmp(portfolio.MaxWeight) > 0 {
				result.notes = append(result.notes, fmt.Sprintf("%s 的现货、永续合成权重绝对值之和 %s 超过 portfolio.max_weight，两边按比例缩小为合计 %s，超出部分留现金", id, total.String(), portfolio.MaxWeight.String()))
				for _, key := range group {
					_, market := SplitTargetKey(key)
					weight := contributions[key].Mul(portfolio.MaxWeight).Div(total)
					result.add(Target{InstrumentID: id, Market: market, Weight: truncateWeightWith(weight, noiseTolerance)})
				}
				continue
			}
		}
		for _, key := range group {
			_, market := SplitTargetKey(key)
			weight := contributions[key]
			if portfolio.HasMaxWeight && len(group) == 1 && abs(weight).Cmp(portfolio.MaxWeight) > 0 {
				capped := portfolio.MaxWeight
				if weight.IsNegative() {
					capped = capped.Neg()
				}
				result.notes = append(result.notes, fmt.Sprintf("%s 的合成权重 %s 超过 portfolio.max_weight，裁剪为 %s，超出部分留现金", id, weight.String(), capped.String()))
				weight = capped
			}
			result.add(Target{InstrumentID: id, Market: market, Weight: truncateWeightWith(weight, noiseTolerance)})
		}
	}
	return result
}

// turnover 是与上期理论目标相比的单边换手：Σ|w_new − w_old| / 2，按（标的, 市场）键比较。
func turnover(targets []Target, previous map[string]string) quant.Decimal {
	current := make(map[string]quant.Decimal, len(targets))
	for _, target := range targets {
		current[TargetKey(target.InstrumentID, target.Market)] = target.Weight
	}
	keys := make(map[string]struct{}, len(current)+len(previous))
	for key := range current {
		keys[key] = struct{}{}
	}
	for key := range previous {
		keys[key] = struct{}{}
	}
	total := quant.Zero()
	for key := range keys {
		old := quant.Zero()
		if raw, ok := previous[key]; ok {
			if parsed, err := quant.Parse(raw); err == nil {
				old = parsed
			}
		}
		total = total.Add(abs(current[key].Sub(old)))
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
