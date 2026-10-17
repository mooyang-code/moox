// Package ops 是因子表达式的算子内核：时序算子按一条序列计算，截面算子按一个截面计算；逐元素算子直接使用
// factorexpr.Scalar。
//
// 所有内核都满足点纯性（设计 5.1）：时刻 t 的输出只由它自己窗口内的输入决定，并按固定顺序计算，与需求区间从哪里
// 开始、是否与别的因子共享、补算怎样分块、工作线程数都无关。因此这里不使用跨点延续状态的滑动累加器——它的舍入误差
// 取决于从哪里开始累加。窗口里只要有 NaN，输出就是 NaN（严格窗口）。
package ops

import (
	"math"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

var nan = math.NaN()

// Series 是一条序列在一段网格下标上的值：网格下标 t 的值是 Data[t-Start]。
type Series struct {
	Data  []float64
	Start int
}

// At 返回网格下标 t 的值。
func (s Series) At(t int) float64 { return s.Data[t-s.Start] }

// Window 返回以 t 结尾、长度为 n 的窗口（从最老到最新）。
func (s Series) Window(t, n int) []float64 { return s.Data[t-n+1-s.Start : t+1-s.Start] }

// SeriesKernel 计算一条序列在网格下标 [outStart, outStart+len(out)) 上的输出；in 与算子的序列参数一一对应。
type SeriesKernel func(out []float64, outStart int, in []Series)

// Rolling 按节点的整数与数值常量参数准备一个时序内核（例如预先算好 EMA 的权重）。
type Rolling func(ints []int, floats []float64) SeriesKernel

// Cross 计算一个截面：values 是同一时刻、同一 series_tag 下各标的的输入（可能含 NaN），结果写入等长的 out。
// 输入为 NaN 的标的不参与计算，输出也为 NaN。
type Cross func(values, out []float64)

// RollingKernel 返回时序算子的内核；不是时序算子时返回 nil。
func RollingKernel(op string) Rolling { return rolling[op] }

// CrossKernel 返回截面算子的内核；不是截面算子时返回 nil。
func CrossKernel(op string) Cross { return cross[op] }

// nanCount 返回以 outStart 结尾、长度为 n 的第一个窗口里的 NaN 个数。之后逐点推进时，移入的加一、移出的减一：
// 计数是整数、精确，与从哪里开始统计无关，不影响点纯性，也不需要额外分配内存。
func nanCount(x Series, outStart, n int) int {
	count := 0
	for t := outStart - n + 1; t <= outStart; t++ {
		if math.IsNaN(x.At(t)) {
			count++
		}
	}
	return count
}

func slideNaN(x Series, t, n, count int) int {
	if math.IsNaN(x.At(t)) {
		count++
	}
	if math.IsNaN(x.At(t - n)) {
		count--
	}
	return count
}

// windowed 对每个输出点取长度为 n 的窗口：窗口含 NaN 输出 NaN，否则输出 fn(窗口) 并把 ±Inf 记为 NaN。
func windowed(n int, fn func(w []float64) float64) SeriesKernel {
	return func(out []float64, outStart int, in []Series) {
		x := in[0]
		nans := nanCount(x, outStart, n)
		for i := range out {
			t := outStart + i
			if i > 0 {
				nans = slideNaN(x, t, n, nans)
			}
			if nans > 0 {
				out[i] = nan
				continue
			}
			out[i] = factorexpr.Finite(fn(x.Window(t, n)))
		}
	}
}

// windowed2 是两个序列参数的版本：任一窗口含 NaN 输出 NaN。
func windowed2(n int, fn func(x, y []float64) float64) SeriesKernel {
	return func(out []float64, outStart int, in []Series) {
		x, y := in[0], in[1]
		nx, ny := nanCount(x, outStart, n), nanCount(y, outStart, n)
		for i := range out {
			t := outStart + i
			if i > 0 {
				nx, ny = slideNaN(x, t, n, nx), slideNaN(y, t, n, ny)
			}
			if nx > 0 || ny > 0 {
				out[i] = nan
				continue
			}
			out[i] = factorexpr.Finite(fn(x.Window(t, n), y.Window(t, n)))
		}
	}
}

func byWindow(fn func(n int) func(w []float64) float64) Rolling {
	return func(ints []int, _ []float64) SeriesKernel {
		n := ints[len(ints)-1]
		return windowed(n, fn(n))
	}
}

var rolling = map[string]Rolling{
	"ts_sum":  byWindow(func(int) func([]float64) float64 { return exactSum }),
	"ts_mean": byWindow(func(int) func([]float64) float64 { return func(w []float64) float64 { m, _ := mean(w); return m } }),
	"ts_min":  byWindow(func(int) func([]float64) float64 { return windowMin }),
	"ts_max":  byWindow(func(int) func([]float64) float64 { return windowMax }),
	"ts_product": byWindow(func(int) func([]float64) float64 {
		return func(w []float64) float64 {
			product := 1.0
			for _, v := range w {
				product *= v
			}
			return product
		}
	}),
	"ts_var": byWindow(func(int) func([]float64) float64 { return variance }),
	"ts_std": byWindow(func(int) func([]float64) float64 {
		return func(w []float64) float64 { return math.Sqrt(variance(w)) }
	}),
	"ts_zscore": byWindow(func(int) func([]float64) float64 {
		return func(w []float64) float64 {
			m, constant := mean(w)
			if constant {
				return nan
			}
			sd := math.Sqrt(variance(w))
			if sd == 0 {
				return nan
			}
			return (w[len(w)-1] - m) / sd
		}
	}),
	"ts_rank":         byWindow(func(int) func([]float64) float64 { return rankLast }),
	"ts_skew":         byWindow(func(int) func([]float64) float64 { return skew }),
	"ts_kurt":         byWindow(func(int) func([]float64) float64 { return kurt }),
	"ts_argmax":       byWindow(func(int) func([]float64) float64 { return argExtreme(func(a, b float64) bool { return a > b }) }),
	"ts_argmin":       byWindow(func(int) func([]float64) float64 { return argExtreme(func(a, b float64) bool { return a < b }) }),
	"ts_decay_linear": byWindow(func(n int) func([]float64) float64 { return decayLinear(n) }),
	"ts_count": byWindow(func(int) func([]float64) float64 {
		return func(w []float64) float64 {
			count := 0.0
			for _, v := range w {
				if v != 0 {
					count++
				}
			}
			return count
		}
	}),
	"ts_slope":   byWindow(func(int) func([]float64) float64 { return func(w []float64) float64 { return regression(w).slope } }),
	"ts_rsquare": byWindow(func(int) func([]float64) float64 { return func(w []float64) float64 { return regression(w).rsquare } }),
	"ts_resi":    byWindow(func(int) func([]float64) float64 { return func(w []float64) float64 { return regression(w).resi } }),
	"ts_corr": func(ints []int, _ []float64) SeriesKernel {
		return windowed2(ints[0], correlation)
	},
	"ts_cov": func(ints []int, _ []float64) SeriesKernel {
		return windowed2(ints[0], covariance)
	},
	"ts_quantile": func(ints []int, floats []float64) SeriesKernel {
		q := floats[0]
		return sortedWindow(ints[0], func(sorted []float64) float64 { return quantileLinear(sorted, q) })
	},
	"ts_median": func(ints []int, _ []float64) SeriesKernel {
		return sortedWindow(ints[0], median)
	},
	"ema": func(ints []int, _ []float64) SeriesKernel {
		return truncatedSmoothing(factorexpr.EMAAlpha(ints[0]))
	},
	"sma": func(ints []int, _ []float64) SeriesKernel {
		return truncatedSmoothing(factorexpr.SMAAlpha(ints[0], ints[1]))
	},
	"delay": func(ints []int, _ []float64) SeriesKernel {
		k := ints[0]
		return func(out []float64, outStart int, in []Series) {
			for i := range out {
				out[i] = in[0].At(outStart + i - k)
			}
		}
	},
	"delta": func(ints []int, _ []float64) SeriesKernel {
		k := ints[0]
		return func(out []float64, outStart int, in []Series) {
			for i := range out {
				t := outStart + i
				out[i] = factorexpr.Finite(in[0].At(t) - in[0].At(t-k))
			}
		}
	},
	"pct_change": func(ints []int, _ []float64) SeriesKernel {
		k := ints[0]
		return func(out []float64, outStart int, in []Series) {
			for i := range out {
				t := outStart + i
				current, previous := in[0].At(t), in[0].At(t-k)
				if math.IsNaN(current) || math.IsNaN(previous) || previous == 0 {
					out[i] = nan
					continue
				}
				out[i] = factorexpr.Finite(current/previous - 1)
			}
		}
	},
}
