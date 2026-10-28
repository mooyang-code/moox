package ops

import (
	"math"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// 序列开头允许窗口没攒满的宽松窗口算子（*_relaxed）：窗口里的 NaN 只出现在开头（序列还没开始）时，在剩下的有效值上计算，
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

// relaxedWindowed 对每个输出点取长度为 n 的窗口，有效段不少于 m 个值时输出 fn(有效段)，否则输出 NaN。
func relaxedWindowed(n, m int, fn func(w []float64) float64) SeriesKernel {
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

func byRelaxedWindow(fn func(w []float64) float64) Rolling {
	return func(ints []int, _ []float64) SeriesKernel {
		return relaxedWindowed(ints[0], ints[1], fn)
	}
}

// relaxedSmoothing 是 truncatedSmoothing 的部分窗口版本：窗口最老的有效值作为递推初值。窗口里没有 NaN 时，
// 用与 truncatedSmoothing 完全相同的权重向量与求和路径，结果逐位一致；只有序列开头的部分窗口才按有效段重新取权重。
func relaxedSmoothing(alpha float64, k int) SeriesKernel {
	decay := make([]float64, k) // decay[d] = (1−α)^d，与 truncatedSmoothing 一样逐次相乘得到
	decay[0] = 1
	for d := 1; d < k; d++ {
		decay[d] = decay[d-1] * (1 - alpha)
	}
	weight := make([]float64, k) // weight[d] = α(1−α)^d，d 是距当期的根数
	for d := range weight {
		weight[d] = alpha * decay[d]
	}
	full := make([]float64, k) // 完整窗口的权重（0 为最老），与 truncatedSmoothing 的 weights 相同
	for i := 1; i < k; i++ {
		full[i] = weight[k-1-i]
	}
	full[0] = decay[k-1]
	return func(out []float64, outStart int, in []Series) {
		x := in[0]
		for i := range out {
			w := x.Window(outStart+i, k)
			tail := validTail(w)
			switch {
			case tail == nil:
				out[i] = nan
			case len(tail) == k:
				out[i] = factorexpr.Finite(exactSumOf(k, func(j int) float64 { return full[j] * w[j] }))
			default:
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
}

var relaxedRolling = map[string]Rolling{
	"ts_mean_relaxed": byRelaxedWindow(func(w []float64) float64 { m, _ := mean(w); return m }),
	"ts_std_relaxed":  byRelaxedWindow(func(w []float64) float64 { return math.Sqrt(variance(w)) }),
	"ts_min_relaxed":  byRelaxedWindow(windowMin),
	"ts_max_relaxed":  byRelaxedWindow(windowMax),
	"ema_relaxed": func(ints []int, _ []float64) SeriesKernel {
		alpha := factorexpr.EMAAlpha(ints[0])
		if ints[1] > 0 {
			return relaxedSmoothing(alpha, ints[1])
		}
		return relaxedSmoothing(alpha, factorexpr.RecursiveSpan(alpha))
	},
}

func init() {
	for name, kernel := range relaxedRolling {
		rolling[name] = kernel
	}
}
