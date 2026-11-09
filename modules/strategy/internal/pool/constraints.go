package pool

import (
	"fmt"
	"math"
	"strconv"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 组合约束（设计 P4、P5）与条件转移（P2）：在比例计算之后、合成池目标之前执行，口径是按成员汇总的分配比例
// （多头敞口 = 多头成员的比例之和，空头敞口 = 空头成员的比例之和，不按实际持仓折算）。
//   1. 净敞口区间（exposure）各段按书写顺序执行；
//   2. 总杠杆区间（gross），池的第一根不放大；
//   3. 条件转移（switch），第一个成立的生效，把 from 的比例转给同方向的 to。
// fixed、rotation 下三步都只作用于本期合成用的比例：写给下一期步长限制的上期比例（State.Ratios）仍是步长之后、这三步之前的值。
// 1、2 两步在比例的 float64 副本上计算，算完四舍五入到 12 位小数再换回 Decimal；没写 exposure、gross 时不做这次换算。
// drift 的分配系数本来就是 float64，在漂移与侧内再平衡之后直接执行 1、2 两步（constrainValues），结果写入状态（见 drift.go）。

// constraintTolerance 是区间判断的浮点容差：净敞口超出区间不到它时不修正。
const constraintTolerance = 1e-12

// constrain 对 ratios（步长之后的比例）执行净敞口与总杠杆区间，返回合成用的比例与修正明细；started 为假表示池的第一根（不放大）。
func constrain(cfg Config, ids []string, ratios map[string]quant.Decimal, started bool) (map[string]quant.Decimal, []engine.PoolAdjustment, error) {
	if !cfg.Spec.HasConstraints() {
		return ratios, nil, nil
	}
	values := make(map[string]float64, len(ids))
	for _, id := range ids {
		value, err := strconv.ParseFloat(ratios[id].String(), 64)
		if err != nil {
			return nil, nil, err
		}
		values[id] = value
	}
	adjustments := constrainValues(cfg, ids, values, started)
	out, err := decimalRatios(ids, values)
	if err != nil {
		return nil, nil, err
	}
	return out, adjustments, nil
}

// decimalRatios 把 float64 的比例四舍五入到 12 位小数换回 Decimal（组合约束与 drift 合成前都用这一种换算）。
func decimalRatios(ids []string, values map[string]float64) (map[string]quant.Decimal, error) {
	out := make(map[string]quant.Decimal, len(ids))
	for _, id := range ids {
		value, err := decimalRatio(values[id])
		if err != nil {
			return nil, fmt.Errorf("成员 %s 的比例 %v 无法换成定点数：%w", id, values[id], err)
		}
		out[id] = value
	}
	return out, nil
}

func decimalRatio(value float64) (quant.Decimal, error) {
	return quant.Parse(strconv.FormatFloat(value, 'f', 12, 64))
}

// constrainValues 在比例的 float64 副本 values 上原地执行净敞口区间与总杠杆区间，返回修正明细；started 为假时不放大。
func constrainValues(cfg Config, ids []string, values map[string]float64, started bool) []engine.PoolAdjustment {
	exposure := func() (long, short float64) {
		for _, id := range ids {
			if cfg.Sides[id] == dsl.SideShort {
				short += values[id]
			} else {
				long += values[id]
			}
		}
		return long, short
	}
	scale := func(side string, factor float64) {
		for _, id := range ids {
			if side == "all" || cfg.Sides[id] == side {
				values[id] *= factor
			}
		}
	}
	var adjustments []engine.PoolAdjustment
	if cfg.Spec.Exposure != nil {
		for i, band := range cfg.Spec.Exposure.Bands {
			long, short := exposure()
			gross, net := long+short, long-short
			if (band.HasBelow && !(gross < band.WhenGrossBelow)) || (band.HasAbove && !(gross > band.WhenGrossAbove)) {
				continue
			}
			if net <= band.Max+constraintTolerance && net >= band.Min-constraintTolerance {
				continue
			}
			reduce := band.Adjust == dsl.AdjustReduceLarger || (band.Adjust == dsl.AdjustAuto && gross > band.Pivot)
			// 按超出方向修正：净敞口高于 max 时缩多头或放大空头，低于 min 时缩空头或放大多头。
			above := net > band.Max
			side, current, target := dsl.SideLong, long, 0.0
			switch {
			case above && reduce:
				target = short + band.Max
			case above:
				side, current, target = dsl.SideShort, short, long-band.Max
			case reduce:
				side, current, target = dsl.SideShort, short, long-band.Min
			default:
				target = short + band.Min
			}
			adjustment := engine.PoolAdjustment{Kind: "exposure", Band: i + 1, Side: side, LongBefore: long, ShortBefore: short}
			target = math.Max(target, 0)
			if current <= 0 {
				adjustment.Factor = 1
				adjustment.Note = "要缩放的一侧比例为 0，无法修正"
			} else {
				adjustment.Factor = target / current
				scale(side, adjustment.Factor)
			}
			adjustment.LongAfter, adjustment.ShortAfter = exposure()
			adjustments = append(adjustments, adjustment)
		}
	}
	if gross := cfg.Spec.Gross; gross != nil {
		long, short := exposure()
		total := long + short
		factor := 1.0
		switch {
		case gross.HasMax && total > gross.Max+constraintTolerance:
			factor = gross.Max / total
		case gross.HasMin && total < gross.Min-constraintTolerance && started && total > 0:
			factor = gross.Min / total
		}
		if factor != 1 {
			scale("all", factor)
			after := engine.PoolAdjustment{Kind: "gross", Side: "all", Factor: factor, LongBefore: long, ShortBefore: short}
			after.LongAfter, after.ShortAfter = exposure()
			adjustments = append(adjustments, after)
		}
	}
	return adjustments
}

// applySwitch 执行条件转移：第一个条件成立（when.empty 的成员本期有效目标为空）的 switch 把 from 的比例全部转给 to，
// 按 to 各自的比例分配（都为 0 时平分），最后一个接收者取剩余，侧的总量精确不变。empty 是本期有效目标为空的成员。
// 返回合成用的比例（新映射，ratios 不变）与生效的转移。
func applySwitch(cfg Config, ratios map[string]quant.Decimal, empty map[string]bool) (map[string]quant.Decimal, *engine.PoolSwitch) {
	for i, entry := range cfg.Spec.Allocation.Switch {
		if !empty[entry.WhenEmpty] {
			continue
		}
		out := make(map[string]quant.Decimal, len(ratios))
		for id, ratio := range ratios {
			out[id] = ratio
		}
		moved := ratios[entry.From]
		weights := make([]quant.Decimal, 0, len(entry.To))
		for _, to := range entry.To {
			weights = append(weights, ratios[to])
		}
		for to, share := range quant.DivideByWeights(moved, entry.To, weights) {
			out[to] = out[to].Add(share)
		}
		out[entry.From] = quant.Zero()
		return out, &engine.PoolSwitch{Index: i, Empty: entry.WhenEmpty, From: entry.From, To: append([]string(nil), entry.To...), Moved: moved.String()}
	}
	return ratios, nil
}
