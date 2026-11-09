package ops

import (
	"math"
	"math/big"
	"math/rand"
	"testing"
)

func sameValue(a, b float64) bool {
	return a == b || (math.IsNaN(a) && math.IsNaN(b))
}

// 聚合广播类内核的手算用例：并列、含 NaN、全 NaN、单样本、布尔输入。
func TestAggregateKernelsByHand(t *testing.T) {
	subjects := []string{"A", "B", "C", "D", "E"}
	ties := []float64{3, 1, nan, 3, 1}
	allNaN := []float64{nan, nan, nan, nan, nan}
	single := []float64{nan, nan, 7, nan, nan}
	cases := []struct {
		op     string
		floats []float64
		values []float64
		want   float64
	}{
		{"cs_mean", nil, ties, 2},
		{"cs_mean", nil, allNaN, nan},
		{"cs_mean", nil, single, 7},
		{"cs_sum", nil, ties, 8},
		{"cs_sum", nil, allNaN, nan},
		{"cs_sum", nil, single, 7},
		{"cs_std", nil, ties, math.Sqrt(4.0 / 3)},
		{"cs_std", nil, single, nan},
		{"cs_std", nil, []float64{2, 2, nan, 2, 2}, 0},
		{"cs_std", nil, allNaN, nan},
		{"cs_median", nil, ties, 2},
		{"cs_median", nil, []float64{5, 1, nan, 3, nan}, 3},
		{"cs_median", nil, single, 7},
		{"cs_median", nil, allNaN, nan},
		{"cs_quantile", []float64{0}, ties, 1},
		{"cs_quantile", []float64{1}, ties, 3},
		{"cs_quantile", []float64{0.25}, ties, 1},
		{"cs_quantile", []float64{0.5}, ties, 2},
		{"cs_quantile", []float64{0.9}, []float64{1, 2, 3, 4, 5}, 4.6},
		{"cs_quantile", []float64{0.3}, single, 7},
		{"cs_quantile", []float64{0.3}, allNaN, nan},
		{"cs_min", nil, ties, 1},
		{"cs_max", nil, ties, 3},
		{"cs_min", nil, allNaN, nan},
		{"cs_max", nil, single, 7},
		// 布尔输入：1 为真、0 为假、NaN 不计；全是 NaN 时没有有效样本，为 NaN（不是 0）。
		{"cs_count", nil, []float64{1, 0, nan, 1, 0}, 2},
		{"cs_count", nil, []float64{0, 0, nan, 0, 0}, 0},
		{"cs_count", nil, allNaN, nan},
		{"cs_count", nil, []float64{nan, nan, 1, nan, nan}, 1},
	}
	for _, c := range cases {
		kernel := AggregateKernel(c.op, c.floats, "")
		if kernel == nil {
			t.Fatalf("%s has no aggregate kernel", c.op)
		}
		got := kernel(Section{Args: [][]float64{c.values}, Subjects: subjects})
		if !sameValue(got, c.want) && math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s%v(%v) = %v, want %v", c.op, c.floats, c.values, got, c.want)
		}
	}
	if AggregateKernel("cs_rank", nil, "") != nil {
		t.Fatal("cs_rank is not an aggregate")
	}
}

// cs_ref 取参考标的那一行的值；参考标的不在截面里或它的值为 NaN 时为 NaN。
func TestAggregateReference(t *testing.T) {
	subjects := []string{"A", "B", "C"}
	ref := AggregateKernel("cs_ref", nil, "B")
	if got := ref(Section{Args: [][]float64{{1, 2, 3}}, Subjects: subjects}); got != 2 {
		t.Fatalf("cs_ref = %v, want 2", got)
	}
	if got := ref(Section{Args: [][]float64{{1, nan, 3}}, Subjects: subjects}); !math.IsNaN(got) {
		t.Fatalf("cs_ref of NaN reference = %v, want NaN", got)
	}
	if got := AggregateKernel("cs_ref", nil, "Z")(Section{Args: [][]float64{{1, 2, 3}}, Subjects: subjects}); !math.IsNaN(got) {
		t.Fatalf("cs_ref of missing reference = %v, want NaN", got)
	}
	if got := ref(Section{Args: [][]float64{nil}}); !math.IsNaN(got) {
		t.Fatalf("cs_ref of empty section = %v, want NaN", got)
	}
}

// 随机截面上，聚合结果与参考实现一致：均值、标准差按 256 位精确求和，分位数按排序后的定义。
func TestAggregateKernelsMatchReference(t *testing.T) {
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		values := randomSeries(rng, 1+rng.Intn(30))
		subjects := make([]string, len(values))
		var valid []float64
		for i, v := range values {
			subjects[i] = string(rune('A' + i))
			if !math.IsNaN(v) {
				valid = append(valid, v)
			}
		}
		check := func(op string, floats []float64, want float64, tol float64) {
			got := AggregateKernel(op, floats, "")(Section{Args: [][]float64{values}, Subjects: subjects})
			if math.IsNaN(want) || math.IsNaN(got) {
				if !(math.IsNaN(want) && math.IsNaN(got)) {
					t.Fatalf("seed %d %s = %v, want %v", seed, op, got, want)
				}
				return
			}
			if math.Abs(got-want) > tol*math.Max(1, math.Abs(want)) {
				t.Fatalf("seed %d %s = %v, want %v", seed, op, got, want)
			}
		}
		if len(valid) == 0 {
			for _, op := range []string{"cs_mean", "cs_sum", "cs_std", "cs_median", "cs_min", "cs_max", "cs_count"} {
				check(op, nil, nan, 0)
			}
			check("cs_quantile", []float64{0.5}, nan, 0)
			continue
		}
		check("cs_mean", nil, toFloat(bigMean(valid)), 1e-15)
		check("cs_sum", nil, toFloat(bigSum(len(valid), func(i int) *big.Float { return bigFloat(valid[i]) })), 0)
		if len(valid) >= 2 {
			check("cs_std", nil, math.Sqrt(refVariance(valid)), 1e-12)
		} else {
			check("cs_std", nil, nan, 0)
		}
		for _, q := range []float64{0, 0.1, 0.5, 0.75, 1} {
			check("cs_quantile", []float64{q}, refQuantile(valid, q), 0)
		}
		check("cs_median", nil, refQuantile(valid, 0.5), 1e-15)
		check("cs_min", nil, refQuantile(valid, 0), 0)
		check("cs_max", nil, refQuantile(valid, 1), 0)
	}
}

// 逐行变换类（cs_demean、cs_scale）的空值口径：输入为 NaN 的行输出 NaN，其余行照常计算；全是 NaN 时全为 NaN。
func TestRowwiseCrossNaN(t *testing.T) {
	values := []float64{1, nan, 3}
	for op, want := range map[string][]float64{
		"cs_demean": {-1, nan, 1},
		"cs_scale":  {0.25, nan, 0.75},
		"cs_rank":   {0.5, nan, 1},
	} {
		out := make([]float64, len(values))
		CrossKernel(op, nil, "")(Section{Args: [][]float64{values}}, out)
		for i := range want {
			if !sameValue(out[i], want[i]) {
				t.Errorf("%s[%d] = %v, want %v", op, i, out[i], want[i])
			}
		}
		allNaN := []float64{nan, nan}
		out = make([]float64, 2)
		CrossKernel(op, nil, "")(Section{Args: [][]float64{allNaN}}, out)
		if !math.IsNaN(out[0]) || !math.IsNaN(out[1]) {
			t.Errorf("%s of all NaN = %v", op, out)
		}
	}
}
