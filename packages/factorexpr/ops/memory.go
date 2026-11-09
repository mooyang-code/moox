package ops

import "math"

// 窗口内事件记忆：最近 n 根（含当根）里最后一次“事件”的位置。引擎不跨点保存状态，状态类信号用“在窗口里回看最近一次事件”表达；
// 只有最近一次事件落在窗口以内时，结果才等于无限期保持。
//
// 每条序列从第一个输出点的窗口起点开始正向扫描一遍，记下最近一次事件的位置，复杂度 O(输出点数 + n)，不在每个点上回读 n 根。
// 窗口外的事件被丢弃，所以时刻 t 的结果只由 [t−n+1, t] 决定，满足只依赖窗口的要求（设计 5.1）。

// eventMemory 返回事件记忆内核：hit 判断网格下标 t 是否是事件，emit 在最近一次事件位于 last（last ≥ t−n+1）时给出输出；
// 窗口里没有事件时输出 NaN。
func eventMemory(n int, hit func(in []Series, t int) bool, emit func(in []Series, t, last int) float64) SeriesKernel {
	return func(out []float64, outStart int, in []Series) {
		last := math.MinInt
		for t := outStart - n + 1; t < outStart+len(out); t++ {
			if hit(in, t) {
				last = t
			}
			if t < outStart {
				continue
			}
			if last < t-n+1 {
				out[t-outStart] = nan
				continue
			}
			out[t-outStart] = emit(in, t, last)
		}
	}
}

// isTrue 是条件的真值判定：非 NaN 且不为 0。
func isTrue(v float64) bool { return !math.IsNaN(v) && v != 0 }

var memoryRolling = map[string]Rolling{
	// ts_last_valid(x, n)：最近 n 根里最后一个非 NaN 的 x。
	"ts_last_valid": func(ints []int, _ []float64) SeriesKernel {
		return eventMemory(ints[0],
			func(in []Series, t int) bool { return !math.IsNaN(in[0].At(t)) },
			func(in []Series, _, last int) float64 { return in[0].At(last) })
	},
	// ts_bars_since(c, n)：最近 n 根里 c 最后一次为真距今的根数，当根为真记 0（与 ts_argmax 的距今口径相同）。
	"ts_bars_since": func(ints []int, _ []float64) SeriesKernel {
		return eventMemory(ints[0],
			func(in []Series, t int) bool { return isTrue(in[0].At(t)) },
			func(_ []Series, t, last int) float64 { return float64(t - last) })
	},
	// ts_value_when(c, x, n)：最近 n 根里 c 最后一次为真那一根的 x（那一根的 x 是 NaN 时结果也是 NaN）。
	"ts_value_when": func(ints []int, _ []float64) SeriesKernel {
		return eventMemory(ints[0],
			func(in []Series, t int) bool { return isTrue(in[0].At(t)) },
			func(in []Series, _, last int) float64 { return in[1].At(last) })
	},
}

func init() {
	for name, kernel := range memoryRolling {
		rolling[name] = kernel
	}
}
