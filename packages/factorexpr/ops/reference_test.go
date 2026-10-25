package ops

import (
	"math"
	"math/big"
	"math/rand"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// 参考实现：按定义直接计算，求和用 256 位的 big.Float，与内核的实现方式无关，只用于测试。

const precision = 256

func bigFloat(v float64) *big.Float { return new(big.Float).SetPrec(precision).SetFloat64(v) }

func bigSum(n int, fn func(i int) *big.Float) *big.Float {
	sum := bigFloat(0)
	for i := 0; i < n; i++ {
		sum.Add(sum, fn(i))
	}
	return sum
}

func toFloat(v *big.Float) float64 {
	f, _ := v.Float64()
	return f
}

func bigMean(w []float64) *big.Float {
	m := bigSum(len(w), func(i int) *big.Float { return bigFloat(w[i]) })
	return m.Quo(m, bigFloat(float64(len(w))))
}

// bigMoment 返回 Σ(x − mean)^p（精确均值）。
func bigMoment(w []float64, p int) *big.Float {
	m := bigMean(w)
	return bigSum(len(w), func(i int) *big.Float {
		d := new(big.Float).SetPrec(precision).Sub(bigFloat(w[i]), m)
		result := bigFloat(1)
		for k := 0; k < p; k++ {
			result.Mul(result, d)
		}
		return result
	})
}

func refVariance(w []float64) float64 {
	m2 := bigMoment(w, 2)
	return toFloat(m2.Quo(m2, bigFloat(float64(len(w)-1))))
}

func refSkew(w []float64) float64 {
	n := float64(len(w))
	m2, m3 := toFloat(bigMoment(w, 2)), toFloat(bigMoment(w, 3))
	if m2 == 0 {
		return nan
	}
	b, c := m2/n, m3/n
	return math.Sqrt(n*(n-1)) * c / ((n - 2) * math.Pow(b, 1.5))
}

func refKurt(w []float64) float64 {
	n := float64(len(w))
	m2, m4 := toFloat(bigMoment(w, 2)), toFloat(bigMoment(w, 4))
	if m2 == 0 {
		return nan
	}
	b, d := m2/n, m4/n
	return ((n*n-1)*d/(b*b) - 3*(n-1)*(n-1)) / ((n - 2) * (n - 3))
}

func refCov(x, y []float64) float64 {
	mx, my := bigMean(x), bigMean(y)
	s := bigSum(len(x), func(i int) *big.Float {
		dx := new(big.Float).SetPrec(precision).Sub(bigFloat(x[i]), mx)
		dy := new(big.Float).SetPrec(precision).Sub(bigFloat(y[i]), my)
		return dx.Mul(dx, dy)
	})
	return toFloat(s.Quo(s, bigFloat(float64(len(x)-1))))
}

func refCorr(x, y []float64) float64 {
	vx, vy := refVariance(x), refVariance(y)
	if vx == 0 || vy == 0 {
		return nan
	}
	return refCov(x, y) / math.Sqrt(vx*vy)
}

func refRegression(w []float64) fit {
	n := len(w)
	ym := bigMean(w)
	xm := bigFloat(float64(n+1) / 2)
	sxy := bigSum(n, func(i int) *big.Float {
		dx := new(big.Float).SetPrec(precision).Sub(bigFloat(float64(i+1)), xm)
		dy := new(big.Float).SetPrec(precision).Sub(bigFloat(w[i]), ym)
		return dx.Mul(dx, dy)
	})
	syy := toFloat(bigMoment(w, 2))
	sxx := float64(n) * (float64(n)*float64(n) - 1) / 12
	slope := toFloat(sxy) / sxx
	result := fit{slope: slope, resi: w[n-1] - (toFloat(ym) + slope*(float64(n)-float64(n+1)/2))}
	if syy == 0 {
		result.rsquare = nan
	} else {
		result.rsquare = toFloat(sxy) * toFloat(sxy) / (sxx * syy)
	}
	return result
}

func refQuantile(w []float64, q float64) float64 {
	sorted := append([]float64(nil), w...)
	sort.Float64s(sorted)
	return quantileLinear(sorted, q)
}

func refEMA(w []float64, alpha float64) float64 {
	y := bigFloat(w[0])
	a, keep := bigFloat(alpha), bigFloat(1-alpha)
	for _, v := range w[1:] {
		term := new(big.Float).SetPrec(precision).Mul(a, bigFloat(v))
		y.Mul(y, keep).Add(y, term)
	}
	return toFloat(y)
}

type refCase struct {
	op     string
	floats []float64
	args   int
	ref    func(w []float64, v []float64, n int) float64
	tol    float64 // 0 表示必须逐位相同
	// floor 是相对误差分母的下限：无量纲的统计量（偏度、相关系数等）取 1，数据量级的取窗口的数据量级；nil 表示纯相对误差。
	floor func(w []float64) float64
}

func unit([]float64) float64 { return 1 }

func dataScale(w []float64) float64 {
	scale := 0.0
	for _, v := range w {
		scale = math.Max(scale, math.Abs(v))
	}
	return scale
}

func window1(fn func(w []float64) float64) func(w, _ []float64, _ int) float64 {
	return func(w, _ []float64, _ int) float64 { return fn(w) }
}

func refCases(n int) []refCase {
	return []refCase{
		{op: "ts_sum", args: 1, tol: 1e-15, ref: window1(func(w []float64) float64 {
			return toFloat(bigSum(len(w), func(i int) *big.Float { return bigFloat(w[i]) }))
		})},
		{op: "ts_mean", args: 1, tol: 1e-15, ref: window1(func(w []float64) float64 { return toFloat(bigMean(w)) })},
		{op: "ts_var", args: 1, tol: 1e-10, ref: window1(refVariance)},
		{op: "ts_std", args: 1, tol: 1e-10, ref: window1(func(w []float64) float64 { return math.Sqrt(refVariance(w)) })},
		{op: "ts_zscore", floor: unit, args: 1, tol: 1e-10, ref: window1(func(w []float64) float64 {
			v := refVariance(w)
			if v == 0 {
				return nan
			}
			return (w[len(w)-1] - toFloat(bigMean(w))) / math.Sqrt(v)
		})},
		{op: "ts_skew", floor: unit, args: 1, tol: 1e-9, ref: window1(refSkew)},
		{op: "ts_kurt", floor: unit, args: 1, tol: 1e-9, ref: window1(refKurt)},
		{op: "ts_min", args: 1, ref: window1(func(w []float64) float64 { return refQuantile(w, 0) })},
		{op: "ts_max", args: 1, ref: window1(func(w []float64) float64 { return refQuantile(w, 1) })},
		{op: "ts_median", args: 1, ref: window1(func(w []float64) float64 {
			sorted := append([]float64(nil), w...)
			sort.Float64s(sorted)
			return median(sorted)
		})},
		{op: "ts_quantile", args: 1, floats: []float64{0.3}, ref: window1(func(w []float64) float64 { return refQuantile(w, 0.3) })},
		{op: "ts_rank", args: 1, ref: window1(func(w []float64) float64 {
			sorted := append([]float64(nil), w...)
			sort.Float64s(sorted)
			current := w[len(w)-1]
			first := sort.SearchFloat64s(sorted, current)
			last := first
			for last+1 < len(sorted) && sorted[last+1] == current {
				last++
			}
			return (float64(first+1) + float64(last+1)) / 2 / float64(len(w))
		})},
		{op: "ts_argmax", args: 1, ref: window1(func(w []float64) float64 {
			best := math.Inf(-1)
			index := 0
			for i, v := range w {
				if v > best {
					best, index = v, i
				}
			}
			return float64(len(w) - 1 - index)
		})},
		{op: "ts_count", args: 1, ref: window1(func(w []float64) float64 {
			count := 0
			for _, v := range w {
				if v != 0 {
					count++
				}
			}
			return float64(count)
		})},
		{op: "ts_decay_linear", args: 1, tol: 1e-15, ref: window1(func(w []float64) float64 {
			s := bigSum(len(w), func(i int) *big.Float { return bigFloat(float64(i+1) * w[i]) })
			return toFloat(s) / (float64(len(w)) * float64(len(w)+1) / 2)
		})},
		{op: "ts_slope", floor: dataScale, args: 1, tol: 1e-9, ref: window1(func(w []float64) float64 { return refRegression(w).slope })},
		{op: "ts_rsquare", floor: unit, args: 1, tol: 1e-9, ref: window1(func(w []float64) float64 { return refRegression(w).rsquare })},
		{op: "ts_resi", floor: dataScale, args: 1, tol: 1e-9, ref: window1(func(w []float64) float64 { return refRegression(w).resi })},
		{op: "ts_corr", floor: unit, args: 2, tol: 1e-10, ref: func(w, v []float64, _ int) float64 { return refCorr(w, v) }},
		{op: "ts_cov", args: 2, tol: 1e-10, ref: func(w, v []float64, _ int) float64 { return refCov(w, v) }},
	}
}

// randomSeries 生成带 NaN、常数段与并列值的随机序列。
func randomSeries(rng *rand.Rand, length int) []float64 {
	scale := math.Pow(10, float64(rng.Intn(9)-3))
	values := make([]float64, length)
	level := 100.0
	for i := range values {
		level *= math.Exp(rng.NormFloat64() * 0.02)
		values[i] = level * scale
	}
	switch rng.Intn(4) {
	case 0: // 常数段
		start := rng.Intn(length)
		for i := start; i < min(length, start+rng.Intn(30)+2); i++ {
			values[i] = values[start]
		}
	case 1: // 取整制造并列
		for i := range values {
			values[i] = math.Round(values[i]/scale) * scale
		}
	}
	for i := range values {
		if rng.Float64() < 0.03 {
			values[i] = nan
		}
	}
	return values
}

func hasNaN(w []float64) bool {
	for _, v := range w {
		if math.IsNaN(v) {
			return true
		}
	}
	return false
}

func checkKernel(t *testing.T, rc refCase, n int, x, y []float64, seed int64) {
	t.Helper()
	ints := []int{n}
	kernel := RollingKernel(rc.op)(ints, rc.floats)
	length := len(x)
	outStart := n - 1
	out := make([]float64, length-outStart)
	in := []Series{{Data: x}}
	if rc.args == 2 {
		in = append(in, Series{Data: y})
	}
	kernel(out, outStart, in)
	for i, got := range out {
		tm := outStart + i
		wx := x[tm-n+1 : tm+1]
		var wy []float64
		if rc.args == 2 {
			wy = y[tm-n+1 : tm+1]
		}
		var want float64
		if hasNaN(wx) || (rc.args == 2 && hasNaN(wy)) {
			want = nan
		} else {
			want = factorexpr.Finite(rc.ref(wx, wy, n))
		}
		if math.IsNaN(want) || math.IsNaN(got) {
			if math.IsNaN(want) != math.IsNaN(got) {
				t.Fatalf("seed %d %s(n=%d) t=%d: got %v, want %v", seed, rc.op, n, tm, got, want)
			}
			continue
		}
		if rc.tol == 0 {
			if got != want {
				t.Fatalf("seed %d %s(n=%d) t=%d: got %v, want exactly %v", seed, rc.op, n, tm, got, want)
			}
			continue
		}
		denominator := math.Abs(want)
		if rc.floor != nil {
			denominator = math.Max(denominator, rc.floor(wx))
		}
		if math.Abs(got-want) > rc.tol*denominator && got != want {
			t.Fatalf("seed %d %s(n=%d) t=%d: got %v, want %v (rel %.3g)", seed, rc.op, n, tm, got, want, math.Abs(got-want)/math.Abs(want))
		}
	}
}

func TestKernelsMatchReference(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		rng := rand.New(rand.NewSource(seed))
		length := 20 + rng.Intn(100)
		x, y := randomSeries(rng, length), randomSeries(rng, length)
		for _, rc := range refCases(0) {
			minimum := 1
			switch rc.op {
			case "ts_var", "ts_std", "ts_zscore", "ts_corr", "ts_cov", "ts_slope", "ts_rsquare", "ts_resi":
				minimum = 2
			case "ts_skew":
				minimum = 3
			case "ts_kurt":
				minimum = 4
			}
			n := minimum + rng.Intn(min(40, length)-minimum)
			input := x
			if rc.op == "ts_count" {
				input = make([]float64, length)
				for i, v := range x {
					if math.IsNaN(v) {
						input[i] = nan
					} else if v > y[0] {
						input[i] = 1
					}
				}
			}
			checkKernel(t, rc, n, input, y, seed)
		}
	}
}

// 截断 EMA 与按定义递推（256 位精度）一致。
func TestTruncatedSmoothingMatchesRecursion(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	x := randomSeries(rng, 900)
	for _, alpha := range []float64{factorexpr.EMAAlpha(5), factorexpr.EMAAlpha(20), factorexpr.SMAAlpha(10, 1), 1} {
		k := factorexpr.RecursiveSpan(alpha)
		kernel := truncatedSmoothing(alpha, k)
		out := make([]float64, len(x)-k+1)
		kernel(out, k-1, []Series{{Data: x}})
		for i, got := range out {
			tm := k - 1 + i
			w := x[tm-k+1 : tm+1]
			if hasNaN(w) {
				if !math.IsNaN(got) {
					t.Fatalf("alpha %v t=%d: want NaN, got %v", alpha, tm, got)
				}
				continue
			}
			if want := refEMA(w, alpha); math.Abs(got-want) > 1e-12*math.Abs(want) {
				t.Fatalf("alpha %v t=%d: got %v, want %v", alpha, tm, got, want)
			}
		}
	}
}

func TestShiftKernels(t *testing.T) {
	x := []float64{1, 2, 0, 4, nan, 6, 8}
	// k = 1，输出网格下标 1..6。
	cases := map[string][]float64{
		"delay":      {1, 2, 0, 4, nan, 6},
		"delta":      {1, -2, 4, nan, nan, 2},
		"pct_change": {1, -1, nan, nan, nan, x[6]/x[5] - 1}, // x[3]/x[2]：分母为 0 时为 NaN；最后一项按运行期浮点计算
	}
	for op, want := range cases {
		out := make([]float64, 6)
		RollingKernel(op)([]int{1}, nil)(out, 1, []Series{{Data: x}})
		for i := range want {
			if want[i] != out[i] && !(math.IsNaN(want[i]) && math.IsNaN(out[i])) {
				t.Errorf("%s at t=%d = %v, want %v", op, i+1, out[i], want[i])
			}
		}
	}
}

func TestCrossKernelsMatchReference(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		values := randomSeries(rng, 1+rng.Intn(30))
		indexes := valid(values)
		out := make([]float64, len(values))

		CrossKernel("cs_rank")(values, out)
		for _, i := range indexes {
			less, equal := 0, 0
			for _, j := range indexes {
				if values[j] < values[i] {
					less++
				} else if values[j] == values[i] {
					equal++
				}
			}
			if want := (float64(less) + float64(equal+1)/2) / float64(len(indexes)); out[i] != want {
				t.Fatalf("seed %d cs_rank[%d] = %v, want %v", seed, i, out[i], want)
			}
		}

		valid := make([]float64, 0, len(indexes))
		for _, i := range indexes {
			valid = append(valid, values[i])
		}
		CrossKernel("cs_zscore")(values, out)
		if len(valid) >= 2 && refVariance(valid) > 0 {
			m, sd := toFloat(bigMean(valid)), math.Sqrt(refVariance(valid))
			for _, i := range indexes {
				if want := (values[i] - m) / sd; math.Abs(out[i]-want) > 1e-10*math.Max(1, math.Abs(want)) {
					t.Fatalf("seed %d cs_zscore[%d] = %v, want %v", seed, i, out[i], want)
				}
			}
		} else {
			for _, i := range indexes {
				if !math.IsNaN(out[i]) {
					t.Fatalf("seed %d cs_zscore[%d] = %v, want NaN", seed, i, out[i])
				}
			}
		}
		for i, v := range values {
			if math.IsNaN(v) && !math.IsNaN(out[i]) {
				t.Fatalf("seed %d: NaN input must give NaN output", seed)
			}
		}
	}
}

// 精确求和是正确舍入的结果，并且与顺序无关。
func TestExactSum(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for trial := 0; trial < 500; trial++ {
		values := make([]float64, 1+rng.Intn(200))
		for i := range values {
			values[i] = rng.NormFloat64() * math.Pow(10, float64(rng.Intn(30)-15))
			if rng.Intn(5) == 0 && i > 0 {
				values[i] = -values[i-1] // 制造抵消
			}
		}
		want := toFloat(bigSum(len(values), func(i int) *big.Float { return bigFloat(values[i]) }))
		if got := exactSum(values); got != want {
			t.Fatalf("trial %d: exactSum = %v, want %v", trial, got, want)
		}
		rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
		if got := exactSum(values); got != want {
			t.Fatalf("trial %d: shuffled exactSum = %v, want %v", trial, got, want)
		}
	}
	if exactSum(nil) != 0 {
		t.Fatal("empty sum")
	}
}

// 常数窗口精确处理：均值等于原值，标准差为 0，zscore、偏度、峰度、相关系数为 NaN。
func TestConstantWindows(t *testing.T) {
	w := []float64{0.1, 0.1, 0.1, 0.1, 0.1}
	if m, constant := mean(w); m != 0.1 || !constant {
		t.Fatalf("mean = %v", m)
	}
	if variance(w) != 0 {
		t.Fatalf("variance = %v", variance(w))
	}
	if !math.IsNaN(skew(w)) || !math.IsNaN(kurt(w)) || !math.IsNaN(correlation(w, []float64{1, 2, 3, 4, 5})) {
		t.Fatal("degenerate statistics should be NaN")
	}
	out := make([]float64, 1)
	RollingKernel("ts_zscore")([]int{5}, nil)(out, 4, []Series{{Data: w}})
	if !math.IsNaN(out[0]) {
		t.Fatalf("zscore of constant window = %v", out[0])
	}
	if r := regression(w); r.slope != 0 || r.resi != 0 || !math.IsNaN(r.rsquare) {
		t.Fatalf("regression = %+v", r)
	}
}

func FuzzRollingKernels(f *testing.F) {
	f.Add(int64(1), uint8(5))
	f.Add(int64(42), uint8(17))
	f.Add(int64(-9), uint8(2))
	f.Fuzz(func(t *testing.T, seed int64, window uint8) {
		rng := rand.New(rand.NewSource(seed))
		length := 10 + rng.Intn(60)
		x, y := randomSeries(rng, length), randomSeries(rng, length)
		for _, rc := range refCases(0) {
			n := 4 + int(window)%(length-4)
			if rc.op == "ts_count" {
				continue
			}
			checkKernel(t, rc, n, x, y, seed)
		}
	})
}

// 显式指定 k 的 EMA：span 远大于 k 时仍与「以窗口最老值为初值、按定义递推」一致。
func TestTruncatedSmoothingExplicitSpan(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	x := randomSeries(rng, 600)
	alpha, k := factorexpr.EMAAlpha(1000), 300
	kernel := rolling["ema"]([]int{1000, k}, nil)
	out := make([]float64, len(x)-k+1)
	kernel(out, k-1, []Series{{Data: x}})
	for i, got := range out {
		tm := k - 1 + i
		w := x[tm-k+1 : tm+1]
		if hasNaN(w) {
			continue
		}
		if want := refEMA(w, alpha); math.Abs(got-want) > 1e-12*math.Abs(want) {
			t.Fatalf("t=%d: got %v, want %v", tm, got, want)
		}
	}
}
