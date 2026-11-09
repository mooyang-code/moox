package ops

import (
	"math"
	"math/big"
	"math/rand"
	"sort"
	"testing"
)

// relaxedCase 是一个宽松窗口算子与它的严格版本。ref 按定义在有效尾段上计算期望值（参考实现与内核无关）。
type relaxedCase struct {
	op, strict string
	minimum    int
	floats     []float64
	inputs     int  // 序列参数个数：1 或 2
	boolean    bool // 输入是布尔序列（ts_count_relaxed）
	ref        func(x, y []float64) float64
	tol        float64
}

func relaxedCases() []relaxedCase {
	sorted := func(w []float64) []float64 {
		out := append([]float64(nil), w...)
		sort.Float64s(out)
		return out
	}
	return []relaxedCase{
		{op: "ts_sum_relaxed", strict: "ts_sum", minimum: 1, inputs: 1,
			ref: func(x, _ []float64) float64 { return toFloat(bigSum(len(x), func(i int) *big.Float { return bigFloat(x[i]) })) }},
		{op: "ts_count_relaxed", strict: "ts_count", minimum: 1, inputs: 1, boolean: true,
			ref: func(x, _ []float64) float64 {
				count := 0.0
				for _, v := range x {
					if v != 0 {
						count++
					}
				}
				return count
			}},
		{op: "ts_median_relaxed", strict: "ts_median", minimum: 1, inputs: 1,
			ref: func(x, _ []float64) float64 { return refQuantile(x, 0.5) }, tol: 1e-15},
		{op: "ts_quantile_relaxed", strict: "ts_quantile", minimum: 1, inputs: 1, floats: []float64{0.3},
			ref: func(x, _ []float64) float64 { return quantileLinear(sorted(x), 0.3) }},
		{op: "ts_rank_relaxed", strict: "ts_rank", minimum: 1, inputs: 1,
			ref: func(x, _ []float64) float64 {
				current, less, equal := x[len(x)-1], 0, 0
				for _, v := range x {
					if v < current {
						less++
					} else if v == current {
						equal++
					}
				}
				return (float64(less) + float64(equal+1)/2) / float64(len(x))
			}},
		{op: "ts_var_relaxed", strict: "ts_var", minimum: 2, inputs: 1, ref: func(x, _ []float64) float64 { return refVariance(x) }, tol: 1e-12},
		{op: "ts_zscore_relaxed", strict: "ts_zscore", minimum: 2, inputs: 1,
			ref: func(x, _ []float64) float64 {
				sd := math.Sqrt(refVariance(x))
				if sd == 0 {
					return nan
				}
				return (x[len(x)-1] - toFloat(bigMean(x))) / sd
			}, tol: 1e-10},
		{op: "ts_skew_relaxed", strict: "ts_skew", minimum: 3, inputs: 1, ref: func(x, _ []float64) float64 { return refSkew(x) }, tol: 1e-9},
		{op: "ts_kurt_relaxed", strict: "ts_kurt", minimum: 4, inputs: 1, ref: func(x, _ []float64) float64 { return refKurt(x) }, tol: 1e-9},
		{op: "ts_argmax_relaxed", strict: "ts_argmax", minimum: 1, inputs: 1,
			ref: func(x, _ []float64) float64 {
				best := 0
				for i, v := range x {
					if v > x[best] {
						best = i
					}
				}
				return float64(len(x) - 1 - best)
			}},
		{op: "ts_argmin_relaxed", strict: "ts_argmin", minimum: 1, inputs: 1,
			ref: func(x, _ []float64) float64 {
				best := 0
				for i, v := range x {
					if v < x[best] {
						best = i
					}
				}
				return float64(len(x) - 1 - best)
			}},
		{op: "ts_product_relaxed", strict: "ts_product", minimum: 1, inputs: 1,
			ref: func(x, _ []float64) float64 {
				product := 1.0
				for _, v := range x {
					product *= v / 100
				}
				return product
			}, tol: 1e-12},
		{op: "ts_corr_relaxed", strict: "ts_corr", minimum: 2, inputs: 2, ref: refCorr, tol: 1e-10},
		{op: "ts_cov_relaxed", strict: "ts_cov", minimum: 2, inputs: 2, ref: refCov, tol: 1e-10},
	}
}

// relaxedInputs 返回一组输入序列：ts_product 按 x/100 计算（避免连乘溢出），布尔算子的输入是 x 是否高于 100。
func relaxedInputs(c relaxedCase, x, y []float64) []Series {
	in := []Series{{Data: x}}
	switch {
	case c.boolean:
		b := make([]float64, len(x))
		for i, v := range x {
			switch {
			case math.IsNaN(v):
				b[i] = nan
			case v > 100:
				b[i] = 1
			}
		}
		in[0] = Series{Data: b}
	case c.op == "ts_product_relaxed":
		scaled := make([]float64, len(x))
		for i, v := range x {
			scaled[i] = v / 100
		}
		in[0] = Series{Data: scaled}
	}
	if c.inputs == 2 {
		in = append(in, Series{Data: y})
	}
	return in
}

func runRelaxed(c relaxedCase, op string, ints []int, in []Series, n int) []float64 {
	out := make([]float64, len(in[0].Data)-n+1)
	RollingKernel(op)(ints, c.floats)(out, n-1, in)
	return out
}

func closeTo(got, want, tol float64) bool {
	if math.IsNaN(got) || math.IsNaN(want) {
		return math.IsNaN(got) && math.IsNaN(want)
	}
	return math.Abs(got-want) <= tol*math.Max(1, math.Abs(want))
}

// 性质：窗口里没有 NaN 时，宽松算子与对应的严格算子逐位一致（共用同一个统计函数），m 取任何合法值都一样。
func TestRelaxedEqualsStrictWithoutNaN(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	x, y := headSeries(rng, 0, 300), headSeries(rng, 0, 300)
	for _, c := range relaxedCases() {
		in := relaxedInputs(c, x, y)
		for _, n := range []int{max(c.minimum, 4), 9, 60} {
			want := runRelaxed(c, c.strict, []int{n}, in, n)
			for _, m := range []int{c.minimum, n} {
				got := runRelaxed(c, c.op, []int{n, m}, in, n)
				for i := range got {
					if math.IsNaN(got[i]) != math.IsNaN(want[i]) || (!math.IsNaN(got[i]) && math.Float64bits(got[i]) != math.Float64bits(want[i])) {
						t.Fatalf("%s(n=%d, m=%d)[%d] = %v, %s = %v", c.op, n, m, i, got[i], c.strict, want[i])
					}
				}
			}
		}
	}
}

// 序列开头：窗口里开头的 NaN 不计，有效段不少于 m 个时按定义在有效段上计算；不足 m 个时为 NaN（m 的边界）。
// 两个序列参数时，从两条序列都开始的位置起取共同的有效尾段。
func TestRelaxedAllHeadMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	const n, leadX, leadY = 30, 40, 47
	x, y := headSeries(rng, leadX, 160), headSeries(rng, leadY, 160)
	for _, c := range relaxedCases() {
		in := relaxedInputs(c, x, y)
		lead := leadX
		if c.inputs == 2 {
			lead = leadY
		}
		for _, m := range []int{c.minimum, c.minimum + 3} {
			out := runRelaxed(c, c.op, []int{n, m}, in, n)
			for i, got := range out {
				tm := n - 1 + i
				start := max(tm-n+1, lead)
				valid := tm - start + 1
				if valid < m {
					if !math.IsNaN(got) {
						t.Fatalf("%s(m=%d) t=%d: 有效值 %d < m，应为 NaN，得到 %v", c.op, m, tm, valid, got)
					}
					continue
				}
				var yw []float64
				if c.inputs == 2 {
					yw = in[1].Data[start : tm+1]
				}
				want := c.ref(x[start:tm+1], yw)
				if c.boolean || c.op == "ts_product_relaxed" {
					want = c.ref(x[start:tm+1], nil)
					if c.boolean {
						want = c.ref(in[0].Data[start:tm+1], nil)
					}
				}
				if !closeTo(got, want, c.tol) {
					t.Fatalf("%s(m=%d) t=%d: got %v, want %v", c.op, m, tm, got, want)
				}
			}
		}
	}
}

// 有效值之后再出现 NaN（中途缺失）：窗口含这一根时为 NaN；这一根正好是窗口最左一根时按开头的 NaN 处理；滑出窗口后恢复。
func TestRelaxedAllInternalNaN(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	const n, gap = 10, 40
	x, y := headSeries(rng, 3, 80), headSeries(rng, 3, 80)
	x[gap] = nan
	for _, c := range relaxedCases() {
		in := relaxedInputs(c, x, y)
		out := runRelaxed(c, c.op, []int{n, c.minimum}, in, n)
		for i, got := range out {
			tm := n - 1 + i
			switch {
			case tm >= gap && tm-n+1 < gap:
				if !math.IsNaN(got) {
					t.Fatalf("%s t=%d: 窗口中途有 NaN，应为 NaN，得到 %v", c.op, tm, got)
				}
			case tm-n+1 >= gap && tm > gap:
				if math.IsNaN(got) {
					t.Fatalf("%s t=%d: NaN 在窗口左缘或已滑出，不应为 NaN", c.op, tm)
				}
			}
		}
	}
	// 全是 NaN 的窗口为 NaN。
	all := make([]float64, 20)
	for i := range all {
		all[i] = nan
	}
	for _, c := range relaxedCases() {
		in := []Series{{Data: all}}
		if c.inputs == 2 {
			in = append(in, Series{Data: all})
		}
		if out := runRelaxed(c, c.op, []int{10, c.minimum}, in, 10); !math.IsNaN(out[0]) {
			t.Fatalf("%s: 全 NaN 窗口应为 NaN，得到 %v", c.op, out[0])
		}
	}
}
