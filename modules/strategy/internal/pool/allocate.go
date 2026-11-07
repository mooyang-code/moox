package pool

import (
	"math"
	"sort"
	"strconv"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
)

// 资金分配（设计 3.5，每期计算顺序的第 3～5 步）：本期的比例只用到上一期末为止的成员净值序列，
// 分配方式给出基础比例 → 乘择时系数 → 按步长限制相对上期比例的变化。模拟爆仓的成员比例一律为 0，不受步长限制。

// allocation 是一期的分配结果，各映射的键都是成员 id。
type allocation struct {
	// base 是分配方式给出的比例；timing 是择时系数，没有择时时为空；before 是乘择时之后、步长之前的比例；ratio 是步长之后的最终比例。
	base, timing, before, ratio map[string]quant.Decimal
	// rank 是轮动的综合名次分，只有 rotation 且历史足够、参与排名的成员才有。
	rank map[string]float64
}

// allocate 用上一期末为止的净值序列 series、上期比例 previous（没有时为 nil）与模拟爆仓标记算出本期的比例。members 是成员顺序。
func allocate(spec dsl.StrategyPool, members []string, series map[string][]float64, bankrupt map[string]bool, previous map[string]quant.Decimal) allocation {
	out := allocation{base: map[string]quant.Decimal{}, before: map[string]quant.Decimal{}, ratio: map[string]quant.Decimal{}, rank: map[string]float64{}}
	switch spec.Allocation.Method {
	case dsl.AllocationRotation:
		out.base, out.rank = rotate(spec.Allocation.Rotation, members, series, bankrupt)
	default:
		for _, id := range members {
			out.base[id] = spec.Allocation.Ratios[id]
		}
	}
	if spec.Timing != nil {
		out.timing = map[string]quant.Decimal{}
		for _, id := range members {
			out.timing[id] = timingCoefficient(series[id], spec.Timing.Window)
		}
	}
	for _, id := range members {
		ratio := out.base[id]
		if out.timing != nil {
			ratio = ratio.Mul(out.timing[id])
		}
		if bankrupt[id] {
			ratio = quant.Zero()
		}
		out.before[id] = ratio
		out.ratio[id] = ratio
		if spec.HasStep && previous != nil && !bankrupt[id] {
			out.ratio[id] = clamp(ratio, previous[id], spec.Step)
		}
	}
	return out
}

// clamp 把比例限制在上期比例 ± step 以内（不为负）。
func clamp(ratio, previous, step quant.Decimal) quant.Decimal {
	upper := previous.Add(step)
	lower := previous.Sub(step)
	if lower.IsNegative() {
		lower = quant.Zero()
	}
	switch {
	case ratio.Cmp(upper) > 0:
		return upper
	case ratio.Cmp(lower) < 0:
		return lower
	}
	return ratio
}

// timingCoefficient 是择时系数：净值（序列最后一个值，即上一期末）严格高于 window 期均线取 1，否则取 0；
// 序列不足 window 期时无从判断，取 1（预热会把序列读满，只在预热起点附近出现）。
func timingCoefficient(series []float64, window int) quant.Decimal {
	if len(series) < window {
		return quant.One()
	}
	last := series[len(series)-1]
	if last > mean(series[len(series)-window:]) {
		return quant.One()
	}
	return quant.Zero()
}

func mean(values []float64) float64 {
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

// rotate 按轮动因子给成员排名：每个因子在未爆仓的成员之间排名（名次从 1 起，并列取平均名次），各因子名次按因子权重加权求和得到综合名次分，
// 综合名次分最小的前 top 名（与第 top 名并列的一并入选）平分 1。爆仓的成员不参与排名，比例为 0；
// 任一入围成员的序列不够长、算不出某个因子时，这一期不排名，未爆仓的成员均分。
func rotate(rotation *dsl.PoolRotation, members []string, series map[string][]float64, bankrupt map[string]bool) (map[string]quant.Decimal, map[string]float64) {
	ratios := make(map[string]quant.Decimal, len(members))
	var eligible []string
	for _, id := range members {
		ratios[id] = quant.Zero()
		if !bankrupt[id] {
			eligible = append(eligible, id)
		}
	}
	if len(eligible) == 0 {
		return ratios, nil
	}
	scores := make(map[string]float64, len(eligible))
	ranked := true
	for _, factor := range rotation.Factors {
		values := make(map[string]float64, len(eligible))
		for _, id := range eligible {
			value, ok := factorValue(factor, series[id])
			if !ok {
				ranked = false
				break
			}
			values[id] = value
		}
		if !ranked {
			break
		}
		weight, _ := strconv.ParseFloat(factor.Weight.String(), 64)
		for id, rank := range factorRanks(values, factor.EffectiveOrder() == dsl.OrderDesc) {
			scores[id] += weight * rank
		}
	}
	if !ranked {
		share := quant.One().Div(quant.Must(strconv.Itoa(len(eligible))))
		for _, id := range eligible {
			ratios[id] = share
		}
		return ratios, nil
	}
	ordered := append([]string(nil), eligible...)
	sort.SliceStable(ordered, func(i, j int) bool { return scores[ordered[i]] < scores[ordered[j]] })
	cut := scores[ordered[min(rotation.Top, len(ordered))-1]]
	var chosen []string
	for _, id := range ordered {
		if scores[id] <= cut+tieTolerance {
			chosen = append(chosen, id)
		}
	}
	share := quant.One().Div(quant.Must(strconv.Itoa(len(chosen))))
	for _, id := range chosen {
		ratios[id] = share
	}
	return ratios, scores
}

// tieTolerance 是综合名次分并列的判定容差（名次与权重的浮点和）。
const tieTolerance = 1e-9

// factorValue 算一个轮动因子在净值序列上的值；序列不够长或值无效（净值不为正）时返回 false。
//   - return(w)：最后一个净值 ÷ w 期之前的净值 − 1，需要 w+1 个值；
//   - ma_ratio(w)：最后一个净值 ÷ 最近 w 个净值的均值 − 1，需要 w 个值；
//   - drawdown(w)：最近 w+1 个净值上的最大回撤（峰值回落的比例，非负），需要 w+1 个值。
func factorValue(factor dsl.RotationFactor, series []float64) (float64, bool) {
	n, w := len(series), factor.Window
	var value float64
	switch factor.Name {
	case dsl.FactorReturn:
		if n < w+1 || series[n-1-w] <= 0 {
			return 0, false
		}
		value = series[n-1]/series[n-1-w] - 1
	case dsl.FactorMARatio:
		if n < w {
			return 0, false
		}
		average := mean(series[n-w:])
		if average <= 0 {
			return 0, false
		}
		value = series[n-1]/average - 1
	case dsl.FactorDrawdown:
		if n < w+1 {
			return 0, false
		}
		peak := 0.0
		for _, equity := range series[n-1-w:] {
			if equity <= 0 {
				return 0, false
			}
			peak = math.Max(peak, equity)
			value = math.Max(value, (peak-equity)/peak)
		}
	default:
		return 0, false
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// factorRanks 给成员按因子值排名：名次从 1 起，descending 为真时值大的靠前；并列取平均名次。
func factorRanks(values map[string]float64, descending bool) map[string]float64 {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if values[ids[i]] != values[ids[j]] {
			if descending {
				return values[ids[i]] > values[ids[j]]
			}
			return values[ids[i]] < values[ids[j]]
		}
		return ids[i] < ids[j]
	})
	ranks := make(map[string]float64, len(ids))
	for start := 0; start < len(ids); {
		end := start + 1
		for end < len(ids) && values[ids[end]] == values[ids[start]] {
			end++
		}
		average := float64(start+1+end) / 2 // 名次 start+1 … end 的平均
		for _, id := range ids[start:end] {
			ranks[id] = average
		}
		start = end
	}
	return ranks
}
