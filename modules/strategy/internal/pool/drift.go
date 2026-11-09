package pool

import (
	"math"
	"sort"
	"strconv"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/ledger"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 漂移分配（设计 P3、P6、P8，每根计算顺序的第 3～7 步）。记号：gᵢ 是成员 i 的分配系数（池目标 = Σᵢ gᵢ·wᵢₖ）；
// vₖ 是成员账本上一次调仓后持仓 k 的带符号价值（现货市值、永续名义价值，Mark 之前账本里的值），rₖ = 本根收盘价 ÷ 上一次估值价 − 1
// （缺价的持仓记 0；池跳过几根时就是这几根的累计收益）；ρᵢ = Σ|vₖ|·rₖ ÷ Σ|vₖ|（空仓为 0），πᵢ = Σvₖ·rₖ ÷ 成员权益。
//   3. 漂移：gᵢ′ = gᵢ·(1 + ρᵢ) / (1 + Σⱼ gⱼ·πⱼ)；写了 drift_by: nav 的一侧，侧的总量照此漂移，侧内构成改按 gᵢ·(1 + πᵢ) 分配；
//   4. 冻结判断（freeze）：被观察成员按 ratios 加权的净值（本根取 Mark 后的估值）在窗口内涨跌幅超过阈值时冻结，冻结中跳过 5～7 步；
//   5. 侧内再平衡（allocation.sides）；
//   6～7. 净敞口与总杠杆区间（constrainValues）。之后的 gᵢ 写入状态，下一根从它漂移；条件转移与单标的上限不写回。
// 全程用 float64，合成前四舍五入到 12 位小数换成 Decimal。模拟爆仓的成员（Mark 之前已爆仓）系数为 0。

// DriftState 是 drift 分配的跨期状态（State.Drift）。
type DriftState struct {
	// G 是各成员的分配系数：第 7 步之后的值，不含条件转移与单标的上限；冻结时是漂移之后的值。
	G map[string]float64 `json:"g"`
	// Start 是漂移起点所在的 bar 序号：没写 allocation.start、initial 与 sides.*.anchor 时是 periodic 的锚点。
	Start int64 `json:"start"`
	// Spread 是各侧（键是 long、short）正在进行的分步再平衡，没有时为空。
	Spread map[string]Spread `json:"spread,omitempty"`
	// FreezeUntil 非 0 时，bar 序号小于它的期处于波动冻结。
	FreezeUntil int64 `json:"freeze_until,omitempty"`
}

// Spread 是一侧正在进行的分步再平衡：Remaining 是还要走的根数，Reason 是触发原因（every_bar、periodic、threshold）。
// 目标构成就是 ratios 的侧内比例（不含爆仓成员），不随状态保存。
type Spread struct {
	Remaining int    `json:"remaining"`
	Reason    string `json:"reason"`
}

// memberReturn 是成员本根的 ρ（持仓名义价值收益率）与 π（资金收益）。
type memberReturn struct{ rho, pi float64 }

// valuationReturn 在 Mark 之前用成员账本的持仓与本根收盘价算 ρ、π（口径见文件开头）；账本为空时都是 0。
func valuationReturn(book *ledger.Ledger, prices map[string]float64) memberReturn {
	if book == nil {
		return memberReturn{}
	}
	keys := make([]string, 0, len(book.Positions))
	for key := range book.Positions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	gross, weighted, signed := 0.0, 0.0, 0.0
	for _, key := range keys {
		position := book.Positions[key]
		r := 0.0
		if price, ok := prices[key]; ok && price > 0 && position.LastPrice > 0 {
			r = price/position.LastPrice - 1
		}
		gross += math.Abs(position.Value)
		weighted += math.Abs(position.Value) * r
		signed += position.Value * r
	}
	var out memberReturn
	if gross > 0 {
		out.rho = weighted / gross
	}
	if equity := book.Equity(); equity > 0 {
		out.pi = signed / equity
	}
	return out
}

// driftResult 是 drift 一期的分配结果。
type driftResult struct {
	// alloc 的 base 是目标比例，before、drifted、ratio 是漂移前、漂移后、写入状态的系数（都换成了 Decimal）；ratio 也是合成用的比例。
	alloc       allocation
	adjustments []engine.PoolAdjustment
	summary     *engine.PoolDrift
	// state 是写入 State.Drift 的新状态；还没到漂移起点时为空。
	state *DriftState
}

// stepDrift 执行 drift 的第 3～7 步。state 是本根之前的池状态（净值序列已补齐到上一根），marks 是各成员本根的估值，
// returns 是各成员的 ρ、π，bankrupt 是 Mark 之前的爆仓标记。
func stepDrift(cfg Config, ids []string, state State, returns map[string]memberReturn, marks map[string]ledger.Outcome, bankrupt map[string]bool, barIndex int64) (driftResult, error) {
	allocation := cfg.Spec.Allocation
	targets := floatRatios(ids, allocation.Ratios, bankrupt)
	previous := state.Drift
	phase := engine.DriftRunning
	switch {
	case cfg.Clock.HasStart && barIndex < cfg.Clock.Start:
		phase = engine.DriftPending
	case previous == nil:
		phase = engine.DriftStart
	}
	var before, g map[string]float64
	next := &DriftState{Start: barIndex}
	if phase == engine.DriftRunning {
		before = copyValues(previous.G)
		g = driftCoefficients(cfg, ids, previous.G, returns)
		next.Start, next.FreezeUntil = previous.Start, previous.FreezeUntil
		for side, spread := range previous.Spread {
			if next.Spread == nil {
				next.Spread = make(map[string]Spread, len(previous.Spread))
			}
			next.Spread[side] = spread
		}
	} else {
		before = floatRatios(ids, allocation.StartRatios(), nil)
		g = copyValues(before)
	}
	for _, id := range ids {
		if bankrupt[id] {
			g[id] = 0
		}
	}
	drifted := copyValues(g)
	summary := &engine.PoolDrift{Phase: phase}
	frozen := false
	if phase == engine.DriftRunning && cfg.Spec.Freeze != nil {
		summary.Freeze = checkFreeze(cfg, ids, state.Series, marks, barIndex, next)
		frozen = summary.Freeze.Frozen
	}
	for _, side := range []string{dsl.SideLong, dsl.SideShort} {
		if sideSummary, ok := rebalanceSide(cfg, ids, side, g, targets, next, phase, frozen, state.LastBarIndex, barIndex); ok {
			summary.Sides = append(summary.Sides, sideSummary)
		}
	}
	var adjustments []engine.PoolAdjustment
	if !frozen {
		// 漂移起点那一根与池的第一根一样不放大总杠杆。
		adjustments = constrainValues(cfg, ids, g, state.Started && phase != engine.DriftStart)
	}
	next.G = copyValues(g)
	out := driftResult{adjustments: adjustments, summary: summary}
	if phase != engine.DriftPending {
		out.state = next
	}
	var err error
	out.alloc = newAllocation()
	for _, id := range ids {
		out.alloc.base[id] = allocation.Ratios[id]
	}
	if out.alloc.before, err = decimalRatios(ids, before); err != nil {
		return driftResult{}, err
	}
	if out.alloc.drifted, err = decimalRatios(ids, drifted); err != nil {
		return driftResult{}, err
	}
	if out.alloc.ratio, err = decimalRatios(ids, g); err != nil {
		return driftResult{}, err
	}
	return out, nil
}

// driftCoefficients 按 P3 的公式漂移一根：gᵢ′ = gᵢ·(1 + ρᵢ) / (1 + Σⱼ gⱼ·πⱼ)；写了 drift_by: nav 的一侧，侧的总量不变，
// 侧内按 gᵢ·(1 + πᵢ) 重新分配（都不为正时保持名义口径）。权益变化的分母不为正（池整体亏光）时全部归零。
func driftCoefficients(cfg Config, ids []string, g map[string]float64, returns map[string]memberReturn) map[string]float64 {
	denominator := 1.0
	for _, id := range ids {
		denominator += g[id] * returns[id].pi
	}
	out := make(map[string]float64, len(ids))
	if !(denominator > 0) {
		for _, id := range ids {
			out[id] = 0
		}
		return out
	}
	for _, id := range ids {
		out[id] = math.Max(g[id]*(1+returns[id].rho)/denominator, 0)
	}
	for _, side := range []string{dsl.SideLong, dsl.SideShort} {
		if cfg.Spec.Side(side).DriftBy != dsl.DriftByNAV {
			continue
		}
		total, weights, sum := 0.0, make(map[string]float64), 0.0
		for _, id := range ids {
			if cfg.Sides[id] != side {
				continue
			}
			total += out[id]
			weights[id] = math.Max(g[id]*(1+returns[id].pi), 0)
			sum += weights[id]
		}
		if !(sum > 0) || !(total > 0) {
			continue
		}
		for _, id := range ids {
			if weight, ok := weights[id]; ok {
				out[id] = total * weight / sum
			}
		}
	}
	return out
}

// checkFreeze 判断波动冻结（P6）：被观察成员的组合净值 = Σ ratiosᵢ × 成员净值，本根取 Mark 后的估值，窗口起点取净值序列里
// window_bars 根之前的值（序列末尾是上一根）。涨跌幅绝对值超过阈值时从本根起冻结 bars 根（冻结中再次触发会重新计时）。
// 历史不足 window_bars 根或窗口起点的净值不为正时不触发。next.FreezeUntil 原地更新，过期的清零。
func checkFreeze(cfg Config, ids []string, series map[string][]float64, marks map[string]ledger.Outcome, barIndex int64, next *DriftState) *engine.PoolFreeze {
	freeze := cfg.Spec.Freeze
	watched := make(map[string]bool, len(freeze.WatchMembers))
	for _, id := range freeze.WatchMembers {
		watched[id] = true
	}
	window := freeze.WindowBars
	now, then, enough := 0.0, 0.0, true
	for _, id := range ids {
		if !watched[id] && (freeze.WatchSide == "" || cfg.Sides[id] != freeze.WatchSide) {
			continue
		}
		ratio, _ := strconv.ParseFloat(cfg.Spec.Allocation.Ratios[id].String(), 64)
		values := series[id]
		if len(values) < window {
			enough = false
			break
		}
		now += ratio * marks[id].EquityBefore
		then += ratio * values[len(values)-window]
	}
	out := &engine.PoolFreeze{}
	if enough && then > 0 {
		change := now/then - 1
		out.Change = &change
		if math.Abs(change) > freeze.Threshold {
			out.Triggered = true
			next.FreezeUntil = barIndex + int64(freeze.Bars)
		}
	}
	if next.FreezeUntil > barIndex {
		out.Frozen, out.Until = true, next.FreezeUntil
	} else {
		next.FreezeUntil = 0
	}
	return out
}

// rebalanceSide 执行一侧的侧内再平衡（P3）：侧的总量保持漂移后的值，只改侧内构成。目标构成是 ratios 的侧内比例（爆仓成员为 0）。
//   - 正在分步时不重新判断触发，本根移动“当前构成到目标的距离 ÷ 剩余根数”，最后一根精确到达；
//   - 没有分步时按方式判断：every_bar 每根触发；periodic 在 (BarIndex − 锚点) ÷ period_bars 的整数部分比上一根推进过的那一根大时触发，
//     落在被跳过的根上的触发点因此在之后第一根求值的 bar 上补触发；threshold 在任一目标不为 0 的成员偏离超过阈值时触发；
//     both 两者都判断，同时成立记 periodic。触发后按 spread_bars 开始分步（1 表示本根一次到位）；
//   - 漂移起点那一根与还没到起点的根不再平衡；冻结期间不触发，分步暂停、不消耗剩余根数。
//
// g 原地更新；没有成员的一侧返回 ok 为假。
func rebalanceSide(cfg Config, ids []string, side string, g, targets map[string]float64, next *DriftState, phase string, frozen bool, lastBar, barIndex int64) (engine.PoolSideRebalance, bool) {
	var members []string
	for _, id := range ids {
		if cfg.Sides[id] == side {
			members = append(members, id)
		}
	}
	if len(members) == 0 {
		return engine.PoolSideRebalance{}, false
	}
	config := cfg.Spec.Side(side)
	out := engine.PoolSideRebalance{Side: side, Rebalance: config.Rebalance}
	total, targetTotal := 0.0, 0.0
	for _, id := range members {
		total += g[id]
		targetTotal += targets[id]
	}
	spread, spreading := next.Spread[side]
	if phase != engine.DriftRunning {
		return out, true
	}
	if frozen {
		if spreading {
			out.Paused, out.Remaining, out.Reason = true, spread.Remaining, spread.Reason
		}
		return out, true
	}
	if !spreading {
		if config.ByThreshold() && total > 0 && targetTotal > 0 {
			deviation := 0.0
			for _, id := range members {
				if targets[id] > 0 {
					deviation = math.Max(deviation, math.Abs((g[id]/total)/(targets[id]/targetTotal)-1))
				}
			}
			out.Deviation = &deviation
		}
		switch {
		case config.Rebalance == dsl.RebalanceEveryBar:
			out.Trigger = dsl.RebalanceEveryBar
		case config.Periodic() && periodicHit(cfg, next, side, config.PeriodBars, lastBar, barIndex):
			out.Trigger = dsl.RebalancePeriodic
		case config.ByThreshold() && out.Deviation != nil && *out.Deviation > config.Threshold:
			out.Trigger = dsl.RebalanceThreshold
		}
		if out.Trigger == "" {
			return out, true
		}
		spread, spreading = Spread{Remaining: config.SpreadBars, Reason: out.Trigger}, true
	}
	if !(total > 0) || !(targetTotal > 0) {
		// 这一侧的系数或目标都为 0：没有构成可调，分步作废。
		delete(next.Spread, side)
		return out, true
	}
	for _, id := range members {
		share, target := g[id]/total, targets[id]/targetTotal
		if spread.Remaining <= 1 {
			g[id] = target * total
		} else {
			g[id] = (share + (target-share)/float64(spread.Remaining)) * total
		}
	}
	spread.Remaining--
	out.Moved, out.Remaining, out.Reason = true, spread.Remaining, spread.Reason
	if spread.Remaining > 0 {
		if next.Spread == nil {
			next.Spread = make(map[string]Spread, 2)
		}
		next.Spread[side] = spread
	} else {
		delete(next.Spread, side)
	}
	return out, true
}

// periodicHit 报告 (lastBar, barIndex] 之间有没有 periodic 的触发点（相位为 0 的根）：锚点取 sides.*.anchor，没写时取漂移起点。
func periodicHit(cfg Config, next *DriftState, side string, period int, lastBar, barIndex int64) bool {
	anchor, ok := cfg.Clock.Anchors[side]
	switch {
	case ok:
	case cfg.Clock.HasStart:
		anchor = cfg.Clock.Start
	default:
		anchor = next.Start
	}
	return floorDiv(barIndex-anchor, int64(period)) > floorDiv(lastBar-anchor, int64(period))
}

func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value%divisor != 0 && (value < 0) != (divisor < 0) {
		quotient--
	}
	return quotient
}

// floatRatios 把成员比例换成 float64；skip 里为真的成员（爆仓）记 0。
func floatRatios(ids []string, ratios map[string]quant.Decimal, skip map[string]bool) map[string]float64 {
	out := make(map[string]float64, len(ids))
	for _, id := range ids {
		value, _ := strconv.ParseFloat(ratios[id].String(), 64)
		if skip[id] {
			value = 0
		}
		out[id] = value
	}
	return out
}

func copyValues(values map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
