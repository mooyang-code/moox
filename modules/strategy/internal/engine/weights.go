package engine

import (
	"math/big"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// allocate 把规则预算分配给按名次排列（最优在前）的选中标的。
// equal 平分；rank 按名次线性配权（最优 N 份、最差 1 份）；cap 是规则内单标的上限，
// 超出部分在规则内按比例分给未达上限的标的，仍分不出去的留现金。
func allocate(ordered []string, weight dsl.Weight) map[string]quant.Decimal {
	if len(ordered) == 0 {
		return map[string]quant.Decimal{}
	}
	base := baseShares(ordered, weight.Total, weight.Method)
	if !weight.HasCap {
		return base
	}
	return applyCap(ordered, base, weight.Cap)
}

func baseShares(ordered []string, total quant.Decimal, method string) map[string]quant.Decimal {
	if method != dsl.WeightMethodRank {
		return quant.DivideStable(total, ordered)
	}
	// 名次线性配权：最优 N 份、最差 1 份。各份之和恰好等于 total（剩下的最小单位补给名次靠前的），不因逐份截断而少掉零头。
	n := len(ordered)
	parts := make([]int64, n)
	for i := range parts {
		parts[i] = int64(n - i)
	}
	return quant.DivideProportional(total, ordered, parts)
}

// applyCap 把超过 cap 的权重裁到 cap，超出部分按比例分给未达上限的标的（按原权重比例，迭代到没有新的超限），仍分不出去的留现金。
// 再分配用精确的有理数运算，只在最后向零截断一次：逐步用定点数做乘除会让本该恰好等于 cap 的权重差出 1e-12 量级。
func applyCap(ordered []string, weights map[string]quant.Decimal, cap quant.Decimal) map[string]quant.Decimal {
	return applyCapRat(ordered, weights, cap.Rat())
}

func applyCapRat(ordered []string, weights map[string]quant.Decimal, cap *big.Rat) map[string]quant.Decimal {
	current := make(map[string]*big.Rat, len(ordered))
	for _, id := range ordered {
		current[id] = weights[id].Rat()
	}
	capped := make(map[string]struct{}, len(ordered))
	for iteration := 0; iteration < len(ordered)+1; iteration++ {
		excess := new(big.Rat)
		uncapped := make([]string, 0, len(ordered))
		for _, id := range ordered {
			if _, done := capped[id]; done {
				continue
			}
			if current[id].Cmp(cap) > 0 {
				excess.Add(excess, new(big.Rat).Sub(current[id], cap))
				current[id] = new(big.Rat).Set(cap)
				capped[id] = struct{}{}
				continue
			}
			uncapped = append(uncapped, id)
		}
		if excess.Sign() == 0 || len(uncapped) == 0 {
			break
		}
		sum := new(big.Rat)
		for _, id := range uncapped {
			sum.Add(sum, current[id])
		}
		if sum.Sign() == 0 {
			share := new(big.Rat).Quo(excess, new(big.Rat).SetInt64(int64(len(uncapped))))
			for _, id := range uncapped {
				current[id] = new(big.Rat).Add(current[id], share)
			}
			continue
		}
		for _, id := range uncapped {
			added := new(big.Rat).Mul(excess, current[id])
			added.Quo(added, sum)
			current[id] = new(big.Rat).Add(current[id], added)
		}
	}
	for _, id := range ordered {
		weights[id] = quant.FromRat(current[id])
	}
	return weights
}

// orderByRank 把标的按分数排序：top 取降序、bottom 取升序，同分按 instrument_id 升序。
func orderByRank(ids []string, scores map[string]float64, bottom bool) []string {
	ordered := append([]string(nil), ids...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := scores[ordered[i]], scores[ordered[j]]
		if a != b {
			if bottom {
				return a < b
			}
			return a > b
		}
		return ordered[i] < ordered[j]
	})
	return ordered
}

// selectWithBuffer 实现排名缓冲区：上期持有、仍在候选中且全样本名次（与解释明细一致，select.where 之前）
// 不超过 count+buffer 的标的保留，空位按 ordered 的顺序补足。
func selectWithBuffer(ordered []string, rankOf map[string]int, count, buffer int, previouslyHeld []string) []string {
	if count <= 0 || len(ordered) == 0 {
		return nil
	}
	candidate := make(map[string]struct{}, len(ordered))
	for _, id := range ordered {
		candidate[id] = struct{}{}
	}
	kept := make([]string, 0, count)
	for _, id := range previouslyHeld {
		if _, ok := candidate[id]; !ok {
			continue
		}
		if rank, ok := rankOf[id]; ok && rank <= count+buffer {
			kept = append(kept, id)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return rankOf[kept[i]] < rankOf[kept[j]] })
	if len(kept) > count {
		kept = kept[:count]
	}
	selected := make(map[string]struct{}, count)
	for _, id := range kept {
		selected[id] = struct{}{}
	}
	for _, id := range ordered {
		if len(selected) >= count {
			break
		}
		if _, ok := selected[id]; ok {
			continue
		}
		selected[id] = struct{}{}
	}
	result := make([]string, 0, len(selected))
	for _, id := range ordered {
		if _, ok := selected[id]; ok {
			result = append(result, id)
		}
	}
	return result
}
