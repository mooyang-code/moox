package ops

import (
	"math"
	"math/rand"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// 部分窗口序列：开头 lead 个 NaN（序列还没开始），之后是正常的价格序列。
func headSeries(rng *rand.Rand, lead, length int) []float64 {
	x := make([]float64, length)
	price := 100.0
	for i := range x {
		if i < lead {
			x[i] = nan
			continue
		}
		price *= math.Exp(rng.NormFloat64() * 0.02)
		x[i] = price
	}
	return x
}

func runKernel(kernel SeriesKernel, x []float64, n int) []float64 {
	out := make([]float64, len(x)-n+1)
	kernel(out, n-1, []Series{{Data: x}})
	return out
}

// 窗口里没有 NaN 时，relaxed 算子与对应的严格算子逐位一致。
func TestRelaxedEqualsStrictOnFullWindows(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	x := headSeries(rng, 0, 400)
	for _, pair := range [][2]string{{"ts_mean_relaxed", "ts_mean"}, {"ts_std_relaxed", "ts_std"}, {"ts_min_relaxed", "ts_min"}, {"ts_max_relaxed", "ts_max"}} {
		for _, n := range []int{2, 7, 60} {
			got := runKernel(rolling[pair[0]]([]int{n, 2}, nil), x, n)
			want := runKernel(rolling[pair[1]]([]int{n}, nil), x, n)
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s(n=%d)[%d] = %v, %s = %v", pair[0], n, i, got[i], pair[1], want[i])
				}
			}
		}
	}
	for _, args := range [][]int{{20, 0}, {300, 120}} {
		k := args[1]
		if k == 0 {
			k = factorexpr.RecursiveSpan(factorexpr.EMAAlpha(args[0]))
		}
		if k > len(x) {
			continue
		}
		got := runKernel(rolling["ema_relaxed"](args, nil), x, k)
		want := runKernel(rolling["ema"](args, nil), x, k)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("ema_relaxed%v[%d] = %v, ema = %v", args, i, got[i], want[i])
			}
		}
	}
}

// 序列开头：结果与"只用已有的有效值"按定义计算一致（pandas 的 min_periods 与 ewm(adjust=False) 从首根递推）。
func TestRelaxedHeadMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	const n, lead = 30, 45
	x := headSeries(rng, lead, 200)
	cases := []struct {
		op  string
		m   int
		ref func(w []float64) float64
	}{
		{"ts_mean_relaxed", 1, func(w []float64) float64 { return toFloat(bigMean(w)) }},
		{"ts_std_relaxed", 2, func(w []float64) float64 { return math.Sqrt(refVariance(w)) }},
		{"ts_min_relaxed", 1, func(w []float64) float64 { return windowMin(w) }},
		{"ts_max_relaxed", 3, func(w []float64) float64 { return windowMax(w) }},
	}
	for _, c := range cases {
		out := runKernel(rolling[c.op]([]int{n, c.m}, nil), x, n)
		for i, got := range out {
			tm := n - 1 + i
			start := max(tm-n+1, lead)
			valid := tm - start + 1
			if valid < c.m {
				if !math.IsNaN(got) {
					t.Fatalf("%s t=%d: 有效值 %d < m=%d，应为 NaN，得到 %v", c.op, tm, valid, c.m, got)
				}
				continue
			}
			want := c.ref(x[start : tm+1])
			if math.Abs(got-want) > 1e-12*math.Abs(want) {
				t.Fatalf("%s t=%d: got %v, want %v", c.op, tm, got, want)
			}
		}
	}
	alpha := factorexpr.EMAAlpha(20)
	k := 120
	out := runKernel(rolling["ema_relaxed"]([]int{20, k}, nil), x, k)
	for i, got := range out {
		tm := k - 1 + i
		start := max(tm-k+1, lead)
		if start > tm {
			if !math.IsNaN(got) {
				t.Fatalf("ema_relaxed t=%d: 窗口全是 NaN，应为 NaN", tm)
			}
			continue
		}
		// 按 pandas ewm(adjust=False) 的方式从首个有效值逐根递推。
		want := refEMA(x[start:tm+1], alpha)
		if math.Abs(got-want) > 1e-12*math.Abs(want) {
			t.Fatalf("ema_relaxed t=%d: got %v, want %v", tm, got, want)
		}
	}
}

// 有效值之后再出现 NaN（中途缺失）或窗口左缘之后的 NaN：输出 NaN，与严格窗口一致；NaN 滑出窗口后恢复。
func TestRelaxedInternalNaN(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	const n = 10
	x := headSeries(rng, 3, 80)
	x[40] = nan
	for _, op := range []string{"ts_mean_relaxed", "ts_std_relaxed", "ts_min_relaxed", "ts_max_relaxed"} {
		out := runKernel(rolling[op]([]int{n, 2}, nil), x, n)
		for i, got := range out {
			tm := n - 1 + i
			// 窗口 [tm-n+1, tm] 含第 40 根、且第 40 根前面还有有效值：中途 NaN → NaN。
			if tm >= 40 && tm-n+1 < 40 {
				if !math.IsNaN(got) {
					t.Fatalf("%s t=%d: 窗口中途有 NaN，应为 NaN，得到 %v", op, tm, got)
				}
			}
			// 第 40 根正好是窗口最左一根：它属于开头的 NaN，按部分窗口计算。
			if tm-n+1 == 40 && math.IsNaN(got) {
				t.Fatalf("%s t=%d: NaN 位于窗口左缘，应按部分窗口计算", op, tm)
			}
			if tm-n+1 > 40 && math.IsNaN(got) {
				t.Fatalf("%s t=%d: NaN 已滑出窗口，不应为 NaN", op, tm)
			}
		}
	}
	all := make([]float64, 20)
	for i := range all {
		all[i] = nan
	}
	if out := runKernel(rolling["ema_relaxed"]([]int{5, 10}, nil), all, 10); !math.IsNaN(out[0]) {
		t.Fatalf("全 NaN 窗口应为 NaN")
	}
}
