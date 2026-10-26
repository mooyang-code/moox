package ops

import (
	"math"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// 序列开头允许部分窗口的算子（*_partial）：窗口里的 NaN 只出现在开头（序列还没开始）时，在剩下的有效值上计算，
// 相当于 pandas 的 rolling(n, min_periods=m) 与 ewm(adjust=False) 作用在从首根开始的序列上；有效值之后再出现 NaN
// 时输出 NaN，与严格窗口一致（pandas 的 min_periods 会跳过中途的 NaN，这里不跳过）。
// 本地时序存储保证每条序列在 [Earliest, Latest] 上连续，缺失周期写成填充行，所以输入列的 NaN 只会在序列开始之前。
// 结果只取决于固定窗口的内容，仍满足点纯性（设计 5.1）。

// validTail 返回窗口里 NaN 前缀之后的有效段；有效段里还有 NaN 或整个窗口都是 NaN 时返回 nil。
func validTail(w []float64) []float64 {
	start := 0
	for start < len(w) && math.IsNaN(w[start]) {
		start++
	}
	if start == len(w) {
		return nil
	}
	tail := w[start:]
	for _, v := range tail {
		if math.IsNaN(v) {
			return nil
		}
	}
	return tail
}

// partialWindowed 对每个输出点取长度为 n 的窗口，有效段不少于 m 个值时输出 fn(有效段)，否则输出 NaN。
func partialWindowed(n, m int, fn func(w []float64) float64) SeriesKernel {
	return func(out []float64, outStart int, in []Series) {
		x := in[0]
		for i := range out {
			tail := validTail(x.Window(outStart+i, n))
			if len(tail) < m {
				out[i] = nan
				continue
			}
			out[i] = factorexpr.Finite(fn(tail))
		}
	}
}

func byPartialWindow(fn func(w []float64) float64) Rolling {
	return func(ints []int, _ []float64) SeriesKernel {
		return partialWindowed(ints[0], ints[1], fn)
	}
}

// partialSmoothing 是 truncatedSmoothing 的部分窗口版本：窗口最老的有效值作为递推初值。窗口里没有 NaN 时，
// 权重与 truncatedSmoothing 完全相同，结果逐位一致。
func partialSmoothing(alpha float64, k int) SeriesKernel {
	decay := make([]float64, k) // decay[d] = (1−α)^d，与 truncatedSmoothing 一样逐次相乘得到
	decay[0] = 1
	for d := 1; d < k; d++ {
		decay[d] = decay[d-1] * (1 - alpha)
	}
	weight := make([]float64, k) // weight[d] = α(1−α)^d，d 是距当期的根数
	for d := range weight {
		weight[d] = alpha * decay[d]
	}
	return func(out []float64, outStart int, in []Series) {
		x := in[0]
		for i := range out {
			w := x.Window(outStart+i, k)
			tail := validTail(w)
			if tail == nil {
				out[i] = nan
				continue
			}
			last := len(tail) - 1
			out[i] = factorexpr.Finite(exactSumOf(len(tail), func(j int) float64 {
				if j == 0 {
					return decay[last] * tail[0]
				}
				return weight[last-j] * tail[j]
			}))
		}
	}
}

var partialRolling = map[string]Rolling{
	"ts_mean_partial": byPartialWindow(func(w []float64) float64 { m, _ := mean(w); return m }),
	"ts_std_partial":  byPartialWindow(func(w []float64) float64 { return math.Sqrt(variance(w)) }),
	"ts_min_partial":  byPartialWindow(windowMin),
	"ts_max_partial":  byPartialWindow(windowMax),
	"ema_partial": func(ints []int, _ []float64) SeriesKernel {
		alpha := factorexpr.EMAAlpha(ints[0])
		if ints[1] > 0 {
			return partialSmoothing(alpha, ints[1])
		}
		return partialSmoothing(alpha, factorexpr.RecursiveSpan(alpha))
	},
}

func init() {
	for name, kernel := range partialRolling {
		rolling[name] = kernel
	}
}
