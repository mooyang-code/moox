package exec

import (
	"context"
	"math"
	"math/rand"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

func pointers(values []float64) []*float64 {
	out := make([]*float64, len(values))
	for i := range values {
		if !math.IsNaN(values[i]) {
			v := values[i]
			out[i] = &v
		}
	}
	return out
}

// memoryData 是一条合成序列：x、y 是噪声，z 是带持续性的离散状态（1～4），raw 是正的系数。
func memoryData(length int) map[string][]float64 {
	rng := rand.New(rand.NewSource(31))
	x, y, z, raw := make([]float64, length), make([]float64, length), make([]float64, length), make([]float64, length)
	state := 1.0
	for i := 0; i < length; i++ {
		x[i], y[i] = rng.NormFloat64()*1.5, rng.NormFloat64()
		if rng.Float64() < 0.15 {
			state = float64(1 + rng.Intn(4))
		}
		z[i] = state
		raw[i] = 0.5 + rng.Float64()
	}
	return map[string][]float64{"x": x, "y": y, "z": z, "raw": raw}
}

func evalSpec(t *testing.T, spec string, data map[string][]float64, length int) ([]float64, int) {
	t.Helper()
	factor, err := factorexpr.Compile(spec)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	program, err := factorexpr.NewProgram([]factorexpr.ProgramInput{{FactorID: "f", Factor: factor}})
	if err != nil {
		t.Fatal(err)
	}
	columns := map[string][]*float64{}
	for name, values := range data {
		columns[name] = pointers(values)
	}
	panel := singleSeriesPanel(t, length, columns)
	start := program.Lookback - 1
	result, err := Run(context.Background(), program, panel, Options{Start: start, End: length - 1})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]float64, length)
	for i := range out {
		out[i] = math.NaN()
	}
	for tm := start; tm < length; tm++ {
		out[tm] = result.Value(0, 0, tm)
	}
	return out, start
}

// lastEventWithin 返回 [t−n+1, t] 里最后一个满足 hit 的下标；没有返回 −1。
func lastEventWithin(t, n int, hit func(k int) bool) int {
	for k := t; k > t-n && k >= 0; k-- {
		if hit(k) {
			return k
		}
	}
	return -1
}

// 设计 F7 的四种常用写法：滞回、开平仓状态、来路、停留时长，在合成数据上与按定义逐点计算的结果一致；
// 同时统计“窗口内没有事件”的点数（验收时要看这个数）。
func TestEventMemoryIdioms(t *testing.T) {
	const length, n = 400, 30
	data := memoryData(length)
	x, y, z := data["x"], data["y"], data["z"]
	check := func(name, spec string, want func(tm int) float64) {
		t.Helper()
		got, start := evalSpec(t, spec, data, length)
		for tm := start; tm < length; tm++ {
			if w := want(tm); !(got[tm] == w || math.IsNaN(got[tm]) && math.IsNaN(w)) {
				t.Fatalf("%s t=%d: got %v, want %v", name, tm, got[tm], w)
			}
		}
	}
	noEvent := 0
	// 滞回：高于上限记 1，低于（等于）下限记 0，中间保持；窗口里没有事件时按 0。
	check("hysteresis", "output: fill_nan(ts_last_valid(where(x > 3, 1, where(x <= -3, 0, nan)), 30), 0)\n", func(tm int) float64 {
		k := lastEventWithin(tm, n, func(k int) bool { return x[k] > 3 || x[k] <= -3 })
		if k < 0 {
			noEvent++
			return 0
		}
		if x[k] > 3 {
			return 1
		}
		return 0
	})
	if noEvent == 0 || noEvent > length/2 {
		t.Fatalf("hysteresis: %d 个点窗口内没有事件，合成数据应让少量点落在窗口外", noEvent)
	}
	t.Logf("hysteresis: %d 个点窗口内没有事件", noEvent)
	// 开平仓状态：满足开仓记 1，满足平仓记 0，两者同时成立时以外层（开仓）为准。
	check("open/close", "let:\n  open_c: x > 1.5\n  close_c: y < -0.5\noutput: fill_nan(ts_last_valid(where(open_c, 1, where(close_c, 0, nan)), 30), 0)\n",
		func(tm int) float64 {
			k := lastEventWithin(tm, n, func(k int) bool { return x[k] > 1.5 || y[k] < -0.5 })
			if k < 0 || x[k] <= 1.5 {
				return 0
			}
			return 1
		})
	// 来路：当前状态是从哪个状态进入的。
	check("came from", "output: ts_value_when(z != delay(z, 1), delay(z, 1), 30)\n", func(tm int) float64 {
		k := lastEventWithin(tm, n, func(k int) bool { return k >= 1 && z[k] != z[k-1] })
		if k < 0 {
			return math.NaN()
		}
		return z[k-1]
	})
	// 停留时长：距上次状态切换的根数。
	check("dwell", "output: ts_bars_since(z != delay(z, 1), 30)\n", func(tm int) float64 {
		k := lastEventWithin(tm, n, func(k int) bool { return k >= 1 && z[k] != z[k-1] })
		if k < 0 {
			return math.NaN()
		}
		return float64(tm - k)
	})
}

// 状态切换后的系数限制（设计 F7 的示例）：状态从 2/3/4 降到 1 之后 8 根内，系数不超过切换前一根的原始系数；
// 离开状态 1 就提前结束。在合成数据上编译、运行，并按定义逐点核对。
func TestEventMemoryCoefficientLimit(t *testing.T) {
	const length = 400
	data := memoryData(length)
	z, raw := data["z"], data["raw"]
	spec := `let:
  sw: z == 1 and delay(z, 1) >= 2
  t: ts_bars_since(sw, 8)
  stay: ts_bars_since(z != 1, 9) == t + 1
  ref: fill_nan(ts_value_when(sw, delay(raw, 1), 8), raw)
output: where(fill_nan(where(stay, 1, 0), 0) > 0, min(raw, ref), raw)
`
	got, start := evalSpec(t, spec, data, length)
	limited := 0
	for tm := start; tm < length; tm++ {
		want := raw[tm]
		// 最近 8 根里最后一次切换到状态 1 的位置，以及从那以后是否一直在状态 1。
		k := lastEventWithin(tm, 8, func(k int) bool { return k >= 1 && z[k] == 1 && z[k-1] >= 2 })
		if k >= 0 {
			stay := true
			for j := k; j <= tm; j++ {
				stay = stay && z[j] == 1
			}
			// stay 的写法要求最近 9 根里有一次 z != 1（即切换前那一根）：切换前一根 k−1 在窗口内时总成立。
			if stay && tm-(k-1) < 9 {
				want = math.Min(raw[tm], raw[k-1])
			}
		}
		if got[tm] != want {
			t.Fatalf("t=%d: got %v, want %v", tm, got[tm], want)
		}
		if got[tm] < raw[tm] {
			limited++
		}
		if z[tm] != 1 && got[tm] != raw[tm] {
			t.Fatalf("t=%d: 不在状态 1 时不应限制", tm)
		}
	}
	if limited == 0 {
		t.Fatal("合成数据上应至少有一个点被限制")
	}
}
