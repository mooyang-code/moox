package engine

import (
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 单标的分方向上限与超限转移（portfolio.limits）：在多规则相加（或池合成）之后、max_weight 与 leverage 之前执行。
// 一个标的的多头敞口是它各市场正权重之和，空头敞口是负权重绝对值之和；超出上限的部分按比例从这个标的的同方向键上削掉，
// 按方向汇总后转给 overflow.to 指向的来源（规则或成员）本期同方向的其他标的，按该来源的分组权重比例分配。分配后又超限的
// 部分再分一轮，最多 dsl.MaxLimitRounds 轮，剩下的留现金。已经截到上限的标的与冻结标的不再作为去向；去向没有同方向持仓时留现金。

// LimitOverflow 是一次超限截断：标的、方向（long、short）、超出量、轮次（从 1 数）与去向（来源 id；空表示留现金）。
type LimitOverflow struct {
	InstrumentID string `json:"instrument_id"`
	Side         string `json:"side"`
	Excess       string `json:"excess"`
	Round        int    `json:"round"`
	To           string `json:"to,omitempty"`
}

// applyLimits 按 limits 截断 contributions（就地修改）并返回截断明细。groups 是各来源（规则 id 或成员 id）本期的分组目标，
// 键与 contributions 相同；frozen、frozenKeys 是冻结的标的与键，不参与。
func applyLimits(contributions map[string]quant.Decimal, groups map[string]map[string]quant.Decimal, limits *dsl.PortfolioLimits, frozen, frozenKeys map[string]struct{}) []LimitOverflow {
	if limits == nil {
		return nil
	}
	type sideLimit struct {
		side  string
		limit quant.Decimal
		long  bool
	}
	var sides []sideLimit
	if limits.HasLongMax {
		sides = append(sides, sideLimit{side: dsl.SideLong, limit: limits.LongMax, long: true})
	}
	if limits.HasShortMax {
		sides = append(sides, sideLimit{side: dsl.SideShort, limit: limits.ShortMax})
	}
	matches := func(weight quant.Decimal, long bool) bool {
		return !weight.IsZero() && weight.IsNegative() != long
	}
	capped := map[string]bool{}
	var records []LimitOverflow
	for round := 1; ; round++ {
		keys := sortedContributionKeys(contributions)
		movable := round <= dsl.MaxLimitRounds && limits.OverflowTo != ""
		moved := false
		for _, side := range sides {
			excess := quant.Zero()
			first := len(records)
			for i := 0; i < len(keys); {
				id, _ := SplitTargetKey(keys[i])
				j := i
				total := quant.Zero()
				for j < len(keys) {
					if other, _ := SplitTargetKey(keys[j]); other != id {
						break
					}
					if weight := contributions[keys[j]]; matches(weight, side.long) {
						total = total.Add(abs(weight))
					}
					j++
				}
				group := keys[i:j]
				i = j
				if total.Cmp(side.limit) <= 0 {
					continue
				}
				for _, key := range group {
					if weight := contributions[key]; matches(weight, side.long) {
						contributions[key] = weight.Mul(side.limit).Div(total)
					}
				}
				over := total.Sub(side.limit)
				excess = excess.Add(over)
				capped[id+"\x00"+side.side] = true
				records = append(records, LimitOverflow{InstrumentID: id, Side: side.side, Excess: over.String(), Round: round})
			}
			if excess.IsZero() || !movable {
				continue
			}
			// 去向：来源本期同方向的分组目标，去掉已截到上限的标的与冻结标的。
			var destinations []string
			var weights []quant.Decimal
			for _, key := range sortedContributionKeys(groups[limits.OverflowTo]) {
				weight := groups[limits.OverflowTo][key]
				id, _ := SplitTargetKey(key)
				if !matches(weight, side.long) || capped[id+"\x00"+side.side] || contains2(frozen, id) || contains2(frozenKeys, key) {
					continue
				}
				destinations = append(destinations, key)
				weights = append(weights, abs(weight))
			}
			if len(destinations) == 0 {
				continue
			}
			for key, share := range quant.DivideByWeights(excess, destinations, weights) {
				if !side.long {
					share = share.Neg()
				}
				contributions[key] = contributions[key].Add(share)
			}
			for k := first; k < len(records); k++ {
				records[k].To = limits.OverflowTo
			}
			moved = true
		}
		if !moved {
			break
		}
	}
	return records
}

func sortedContributionKeys(values map[string]quant.Decimal) []string {
	keys := make([]string, 0, len(values))
	for key, weight := range values {
		if !weight.IsZero() {
			keys = append(keys, key)
		}
	}
	sortKeys(keys)
	return keys
}
