package ops

import (
	"math"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// 筹码分布：用最近 n 根的开高低收和换手率，推算各价格上的持仓量（成本分布）。
// 只依赖窗口，不在输出点之间保留状态。算法（与东方财富网页的筹码分布同一思路）：
//  1. 窗口最低价到最高价之间取 chipBuckets 个等距价格档；
//  2. 从早到晚逐根处理：各档先乘 (1 − 换手率)，再把当根换手的筹码（总量固定为 换手率/档宽）加到 [最低价, 最高价] 覆盖的档上，
//     各档之间按三角形的高度分配，三角形的顶点在均价 (开+高+低+收)/4；
//  3. 用最后的分布求获利比例（不高于当前收盘价的筹码占比）或某个分位的成本价。
const (
	chipBuckets = 150
	// chipEpsilon 是把价格换算成档下标时的容差，避免正好落在档上的价格因为浮点误差被算到相邻的档。
	chipEpsilon = 1e-9
)

// chipResult 从最终的分布取一个值：grid 是各档的筹码，total 是它们的和，low 与 width 是最低档的价格和档宽，last 是最后一根的收盘价。
type chipResult func(grid []float64, total, low, width, last float64) float64

// chipKernel 是筹码分布算子的内核：序列参数依次是开、高、低、收、换手率（百分比）。任一窗口含 NaN 输出 NaN。
func chipKernel(n int, result chipResult) SeriesKernel {
	return func(out []float64, outStart int, in []Series) {
		var nans [5]int
		for k := range nans {
			nans[k] = nanCount(in[k], outStart, n)
		}
		grid := make([]float64, chipBuckets)
		for i := range out {
			t := outStart + i
			invalid := false
			for k := range nans {
				if i > 0 {
					nans[k] = slideNaN(in[k], t, n, nans[k])
				}
				invalid = invalid || nans[k] > 0
			}
			if invalid {
				out[i] = nan
				continue
			}
			out[i] = factorexpr.Finite(chipValue(in[0].Window(t, n), in[1].Window(t, n), in[2].Window(t, n), in[3].Window(t, n), in[4].Window(t, n), grid, result))
		}
	}
}

// chipValue 算一个窗口的筹码分布并取结果。价格不是正数、最高价低于最低价、窗口内价格没有波动、全程没有换手时返回 NaN。
func chipValue(open, high, low, closes, turnover, grid []float64, result chipResult) float64 {
	lowest, highest := math.Inf(1), math.Inf(-1)
	for i := range closes {
		if !(open[i] > 0 && low[i] > 0 && closes[i] > 0 && high[i] >= low[i]) {
			return nan
		}
		lowest, highest = math.Min(lowest, low[i]), math.Max(highest, high[i])
	}
	if !(highest > lowest) || math.IsInf(highest, 1) {
		return nan
	}
	width := (highest - lowest) / (chipBuckets - 1)
	clear(grid)
	for i := range closes {
		rate := math.Min(math.Max(turnover[i]/100, 0), 1)
		if rate == 0 {
			continue
		}
		for j := range grid {
			grid[j] *= 1 - rate
		}
		average := (open[i] + high[i] + low[i] + closes[i]) / 4
		first := max(int(math.Ceil((low[i]-lowest)/width-chipEpsilon)), 0)
		last := min(int(math.Floor((high[i]-lowest)/width+chipEpsilon)), chipBuckets-1)
		// 每根加入的总量固定为 换手率/档宽，在覆盖到的档之间按三角形的高度分配。
		// 不直接用三角形的密度：振幅接近或小于档宽的 K 线只碰到一两个档点，按密度取会把这一根放大好几倍或整根丢掉。
		amount := rate / width
		weight := 0.0
		for j := first; j <= last; j++ {
			weight += chipTriangle(lowest+float64(j)*width, low[i], high[i], average)
		}
		if weight > 0 {
			for j := first; j <= last; j++ {
				grid[j] += chipTriangle(lowest+float64(j)*width, low[i], high[i], average) / weight * amount
			}
			continue
		}
		// 一档也没覆盖到（一字板、最高最低价都落在两档之间）：全部放进均价所在的档。
		// 覆盖到的档都在三角形的端点上：放进覆盖到的档里离均价最近的一档，与权重极小时的结果一致。
		j := min(max(int(math.Floor((average-lowest)/width+chipEpsilon)), 0), chipBuckets-1)
		if first <= last {
			j = min(max(j, first), last)
		}
		grid[j] += amount
	}
	total := exactSum(grid)
	if !(total > 0) {
		return nan
	}
	return result(grid, total, lowest, width, closes[len(closes)-1])
}

// chipTriangle 返回三角形在 price 处的高度占顶点高度的比例（0～1）：顶点在 average，两端 low、high 处为 0。
func chipTriangle(price, low, high, average float64) float64 {
	ratio := 1.0
	if price <= average {
		if average > low {
			ratio = (price - low) / (average - low)
		}
	} else if high > average {
		ratio = (high - price) / (high - average)
	}
	return math.Min(math.Max(ratio, 0), 1)
}

// chipProfit 是获利比例：价格不高于最后一根收盘价的各档筹码占全部筹码的比例。
func chipProfit(grid []float64, total, low, width, last float64) float64 {
	top := int(math.Floor((last-low)/width + chipEpsilon))
	if top < 0 {
		return 0
	}
	return exactSum(grid[:min(top, chipBuckets-1)+1]) / total
}

// chipCost 返回分位 q 的成本价：从最低档往上累加，累计（含本档）第一次超过 q×全部筹码 的那一档的价格。
func chipCost(q float64) chipResult {
	return func(grid []float64, total, low, width, _ float64) float64 {
		target, sum := q*total, 0.0
		for j, chips := range grid {
			sum += chips
			if sum > target {
				return low + float64(j)*width
			}
		}
		return low + float64(chipBuckets-1)*width
	}
}
