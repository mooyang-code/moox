package ops

import (
	"math"
	"math/rand"
	"testing"
)

// shewchukSum 是改用定点累加器之前的求和实现，作为位级差分的参照。
func shewchukSum(values []float64) float64 {
	var stack [8]float64
	partials := stack[:0]
	for _, x := range values {
		partials = addPartial(partials, x)
	}
	return roundPartials(partials)
}

func accumulatorSum(values []float64) float64 {
	var acc accumulator
	acc.addAll(len(values), func(i int) float64 { return values[i] })
	return acc.round()
}

func sameBits(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Float64bits(a) == math.Float64bits(b)
}

// 定点累加器与 Shewchuk 部分和在有限值与 NaN 上逐位一致（不比较按设计不处理的输入：累加溢出、全为 −0）。
func TestAccumulatorMatchesShewchukBitForBit(t *testing.T) {
	rng := rand.New(rand.NewSource(20261027))
	check := func(name string, values []float64) {
		t.Helper()
		allNegativeZero := len(values) > 0
		for _, v := range values {
			if math.Float64bits(v) != math.Float64bits(math.Copysign(0, -1)) {
				allNegativeZero = false
			}
		}
		if allNegativeZero {
			return
		}
		want, got := shewchukSum(values), accumulatorSum(values)
		if !sameBits(want, got) {
			t.Fatalf("%s：累加器 %v（%#x），Shewchuk %v（%#x），n=%d", name, got, math.Float64bits(got), want, math.Float64bits(want), len(values))
		}
		if !sameBits(want, exactSum(values)) {
			t.Fatalf("%s：exactSum 与参照不一致，n=%d", name, len(values))
		}
	}
	gen := map[string]func(n int) []float64{
		"价格量级": func(n int) []float64 {
			out := make([]float64, n)
			for i := range out {
				out[i] = 100 * (1 + rng.NormFloat64()*0.01)
			}
			return out
		},
		"EMA 加权项": func(n int) []float64 {
			alpha := 2 / float64(rng.Intn(20000)+3)
			out := make([]float64, n)
			w := 1.0
			for i := range out {
				out[i] = alpha * w * (50000 * (1 + rng.NormFloat64()*0.05))
				w *= 1 - alpha
			}
			return out
		},
		"各数量级混合": func(n int) []float64 {
			out := make([]float64, n)
			for i := range out {
				out[i] = math.Ldexp(rng.Float64()-0.5, rng.Intn(400)-200)
			}
			return out
		},
		"大量相消": func(n int) []float64 {
			out := make([]float64, 0, n)
			for len(out)+1 < n {
				v := math.Ldexp(rng.Float64(), rng.Intn(120)-60)
				out = append(out, v, -v*(1+math.Ldexp(rng.Float64(), -40)))
			}
			rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
			return out
		},
		"次正规数": func(n int) []float64 {
			out := make([]float64, n)
			for i := range out {
				out[i] = math.Float64frombits(uint64(rng.Int63n(1<<52))) * float64(1-2*rng.Intn(2))
			}
			return out
		},
		"含零与相同值": func(n int) []float64 {
			out := make([]float64, n)
			for i := range out {
				switch rng.Intn(3) {
				case 0:
					out[i] = 0
				case 1:
					out[i] = 0.1
				default:
					out[i] = -0.1
				}
			}
			return out
		},
	}
	for name, make := range gen {
		for _, n := range []int{1, 2, 3, 31, 32, 33, 34, 100, 1000, 5000, 50000} {
			for trial := 0; trial < 20; trial++ {
				values := make(n)
				check(name, values)
				rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
				check(name+"（打乱）", values)
			}
		}
	}
	// 半数取偶的中点与刚过中点。
	half := math.Ldexp(1, -53)
	for _, values := range [][]float64{
		{1, half}, {1, half, math.Ldexp(1, -300)}, {1, half, -math.Ldexp(1, -300)},
		{1 + math.Ldexp(1, -52), half}, {-1, -half}, {math.Ldexp(1, 900), math.Ldexp(1, 846)},
		{math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64}, {0, 0}, {0.1, -0.1},
		{math.Copysign(0, -1), 0}, {1e300, 1e300, -1e300},
	} {
		padded := append(append([]float64(nil), values...), make34Zeros()...) // 让累加器路径也覆盖到
		check("边界", values)
		check("边界（累加器）", padded)
	}
	// NaN：任一项为 NaN，结果为 NaN。
	for _, n := range []int{5, 100} {
		values := gen["价格量级"](n)
		values[n/2] = math.NaN()
		if !math.IsNaN(exactSum(values)) || !math.IsNaN(shewchukSum(values)) {
			t.Fatalf("含 NaN 的求和应为 NaN，n=%d", n)
		}
	}
}

func make34Zeros() []float64 { return make([]float64, 34) }

// exactSumOf 与 exactSum 在两条路径上都给出相同结果，并按 0…n−1 顺序逐项求值一次。
func TestExactSumOfEvaluatesEachTermOnceInOrder(t *testing.T) {
	for _, n := range []int{10, 1000} {
		values := make([]float64, n)
		for i := range values {
			values[i] = float64(i) * 0.37
		}
		next := 0
		got := exactSumOf(n, func(i int) float64 {
			if i != next {
				t.Fatalf("求值顺序：第 %d 次调用拿到 %d", next, i)
			}
			next++
			return values[i]
		})
		if next != n || !sameBits(got, shewchukSum(values)) {
			t.Fatalf("n=%d：调用 %d 次，结果 %v，参照 %v", n, next, got, shewchukSum(values))
		}
	}
}

func BenchmarkExactSum50000(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	values := make([]float64, 50000)
	alpha := 2 / 4001.0
	w := 1.0
	for i := range values {
		values[i] = alpha * w * (50000 * (1 + rng.NormFloat64()*0.05))
		w *= 1 - alpha
	}
	b.Run("shewchuk", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			shewchukSum(values)
		}
	})
	b.Run("accumulator", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			accumulatorSum(values)
		}
	})
}

func BenchmarkExactSumSmall(b *testing.B) {
	for _, n := range []int{4, 16, 32, 64} {
		values := make([]float64, n)
		for i := range values {
			values[i] = 100 + float64(i)*0.01
		}
		b.Run("shewchuk", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				shewchukSum(values)
			}
		})
		b.Run("accumulator", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				accumulatorSum(values)
			}
		})
	}
}

// emaTerms 生成截断 EMA 的加权项（最老的在前，与 partialSmoothing 的求和顺序相同），价格是随机游走。
func emaTerms(span int, k int) []float64 {
	rng := rand.New(rand.NewSource(int64(span)))
	price := 30000.0
	xs := make([]float64, k)
	for i := range xs {
		price *= math.Exp(rng.NormFloat64() * 0.002)
		xs[i] = price
	}
	alpha := 2 / float64(span+1)
	decay := make([]float64, k)
	decay[0] = 1
	for d := 1; d < k; d++ {
		decay[d] = decay[d-1] * (1 - alpha)
	}
	full := make([]float64, k)
	for i := 1; i < k; i++ {
		full[i] = alpha * decay[k-1-i]
	}
	full[0] = decay[k-1]
	out := make([]float64, k)
	for j := range out {
		out[j] = full[j] * xs[j]
	}
	return out
}

// FH003 的 32 个 Po 成员（n 取自线上会话快照）：短线 ema_partial(close, 2n+1)，长线 ema_partial(close, 6n+1, 50000)。
func BenchmarkFH003Members(b *testing.B) {
	ns := []int{65, 85, 109, 137, 169, 205, 245, 289, 445, 949, 1037, 1129, 1225, 1325, 1429, 1537, 1649, 1765, 1885, 2009, 2137, 2269, 2405, 2545, 2689, 2837, 2989, 3145, 3305, 3637, 3809, 3985}
	var sets [][]float64
	for _, n := range ns {
		short := 2*n + 1
		k := int(math.Ceil(math.Log(1e-9) / math.Log(1-2/float64(short+1))))
		if k > 50000 {
			k = 50000
		}
		sets = append(sets, emaTerms(short, k), emaTerms(6*n+1, 50000))
	}
	b.Run("shewchuk", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, s := range sets {
				shewchukSum(s)
			}
		}
	})
	b.Run("exactSum", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, s := range sets {
				exactSum(s)
			}
		}
	})
}
