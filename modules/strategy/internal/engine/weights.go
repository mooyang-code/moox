package engine

import (
	"fmt"
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
	n := len(ordered)
	denominator := quant.Must(fmt.Sprint(n * (n + 1) / 2))
	result := make(map[string]quant.Decimal, n)
	for i, id := range ordered {
		result[id] = total.Mul(quant.Must(fmt.Sprint(n - i))).Div(denominator)
	}
	return result
}

func applyCap(ordered []string, weights map[string]quant.Decimal, cap quant.Decimal) map[string]quant.Decimal {
	capped := make(map[string]struct{}, len(ordered))
	for iteration := 0; iteration < len(ordered)+1; iteration++ {
		excess := quant.Zero()
		uncapped := make([]string, 0, len(ordered))
		for _, id := range ordered {
			if _, done := capped[id]; done {
				continue
			}
			if weights[id].Cmp(cap) > 0 {
				excess = excess.Add(weights[id].Sub(cap))
				weights[id] = cap
				capped[id] = struct{}{}
				continue
			}
			uncapped = append(uncapped, id)
		}
		if excess.IsZero() || len(uncapped) == 0 {
			break
		}
		sum := quant.Zero()
		for _, id := range uncapped {
			sum = sum.Add(weights[id])
		}
		if sum.IsZero() {
			share := quant.DivideStable(excess, uncapped)
			for _, id := range uncapped {
				weights[id] = weights[id].Add(share[id])
			}
			continue
		}
		for _, id := range uncapped {
			weights[id] = weights[id].Add(excess.Mul(weights[id]).Div(sum))
		}
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

// selectWithBuffer 实现排名缓冲区：上期持有且仍在前 count+buffer 名的标的保留，空位按名次补足。
func selectWithBuffer(ordered []string, count, buffer int, previouslyHeld []string) []string {
	if count <= 0 || len(ordered) == 0 {
		return nil
	}
	rankOf := make(map[string]int, len(ordered))
	for i, id := range ordered {
		rankOf[id] = i + 1
	}
	kept := make([]string, 0, count)
	for _, id := range previouslyHeld {
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

func formatScore(value float64) string {
	return fmt.Sprintf("%.10g", value)
}
