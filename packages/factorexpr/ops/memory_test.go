package ops

import (
	"math"
	"math/rand"
	"testing"
)

func memoryKernel(op string, n int) SeriesKernel { return RollingKernel(op)([]int{n}, nil) }

// runFrom 计算网格下标 [outStart, len−1] 上的输出。
func runFrom(kernel SeriesKernel, outStart int, in ...[]float64) []float64 {
	series := make([]Series, len(in))
	for i, data := range in {
		series[i] = Series{Data: data}
	}
	out := make([]float64, len(in[0])-outStart)
	kernel(out, outStart, series)
	return out
}

func sameSeries(t *testing.T, label string, got, want []float64) {
	t.Helper()
	for i := range want {
		if !sameValue(got[i], want[i]) {
			t.Fatalf("%s[%d] = %v, want %v（全部：%v）", label, i, got[i], want[i], got)
		}
	}
}

// 手算用例：窗口内没有事件、事件恰好在窗口左缘（含）与刚滑出窗口、当根为事件、ts_value_when 取事件那一根的 x。
func TestEventMemoryByHand(t *testing.T) {
	// 下标：        0    1    2    3    4    5    6    7
	c := []float64{nan, 1, 0, 0, 0, 1, nan, 0}
	x := []float64{10, 11, 12, nan, 14, 15, 16, 17}
	const n = 4 // 窗口 [t−3, t]
	// t=3：窗口 [0,3] 里 c 在 1 为真 → 距今 2；t=4：窗口 [1,4] 含 1（左缘）→ 3；t=5：当根为真 → 0；
	// t=6、7：最近一次在 5 → 1、2。
	sameSeries(t, "ts_bars_since", runFrom(memoryKernel("ts_bars_since", n), 3, c), []float64{2, 3, 0, 1, 2})
	sameSeries(t, "ts_value_when", runFrom(memoryKernel("ts_value_when", n), 3, c, x), []float64{11, 11, 15, 15, 15})
	// n=3：t=4 的窗口 [2,4] 里没有事件（1 刚滑出）→ NaN。
	sameSeries(t, "ts_bars_since n=3", runFrom(memoryKernel("ts_bars_since", 3), 3, c), []float64{2, nan, 0, 1, 2})
	// ts_last_valid：t=3 的 x 是 NaN，取 t=2 的 12；之后当根有效取当根。
	sameSeries(t, "ts_last_valid", runFrom(memoryKernel("ts_last_valid", 2), 3, x), []float64{12, 14, 15, 16, 17})
	// 窗口里全是 NaN 时为 NaN；n=1 时就是当根本身。
	gap := []float64{1, nan, nan, nan, 5}
	sameSeries(t, "ts_last_valid gap", runFrom(memoryKernel("ts_last_valid", 3), 2, gap), []float64{1, nan, 5})
	sameSeries(t, "ts_last_valid n=1", runFrom(memoryKernel("ts_last_valid", 1), 0, gap), gap)
	// 事件那一根的 x 是 NaN：结果是 NaN，不往前找别的事件。
	cNaNx := []float64{1, 0, 1, 0}
	xNaN := []float64{7, 8, nan, 9}
	sameSeries(t, "ts_value_when NaN x", runFrom(memoryKernel("ts_value_when", 4), 3, cNaNx, xNaN), []float64{nan})
	// 窗口里没有事件。
	none := []float64{0, 0, nan, 0}
	sameSeries(t, "no event", runFrom(memoryKernel("ts_bars_since", 4), 3, none), []float64{nan})
}

// 随机序列上与“每个点回读 n 根”的朴素定义一致，并且与从哪里开始计算无关（只依赖窗口）。
func TestEventMemoryMatchesNaive(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	const length = 300
	c, x := make([]float64, length), make([]float64, length)
	for i := range c {
		switch r := rng.Float64(); {
		case r < 0.05:
			c[i] = nan
		case r < 0.12:
			c[i] = 1
		}
		x[i] = rng.NormFloat64()
		if rng.Float64() < 0.4 {
			x[i] = nan
		}
	}
	naive := func(op string, n, t int) float64 {
		for k := t; k > t-n; k-- {
			switch op {
			case "ts_last_valid":
				if !math.IsNaN(x[k]) {
					return x[k]
				}
			case "ts_bars_since":
				if isTrue(c[k]) {
					return float64(t - k)
				}
			case "ts_value_when":
				if isTrue(c[k]) {
					return x[k]
				}
			}
		}
		return nan
	}
	for _, op := range []string{"ts_last_valid", "ts_bars_since", "ts_value_when"} {
		for _, n := range []int{1, 2, 7, 40} {
			in := [][]float64{x}
			if op != "ts_last_valid" {
				in = [][]float64{c}
				if op == "ts_value_when" {
					in = append(in, x)
				}
			}
			for _, start := range []int{n - 1, n + 13, length - 1} {
				out := runFrom(memoryKernel(op, n), start, in...)
				for i, got := range out {
					if want := naive(op, n, start+i); !sameValue(got, want) {
						t.Fatalf("%s(n=%d) start=%d t=%d: got %v, want %v", op, n, start, start+i, got, want)
					}
				}
			}
		}
	}
}
