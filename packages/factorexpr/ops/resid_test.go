package ops

import (
	"math"
	"math/rand"
	"testing"
)

// cs_resid 的手算用例：y = 1 + 2x 加上已知残差；残差与 x 正交、和为 0 时回归恰好还原它。
func TestCrossResidualByHand(t *testing.T) {
	x := []float64{-2, -1, 0, 1, 2, nan}
	e := []float64{1, -2, 0, 2, -1, 0} // Σe = 0，Σx·e = −2+2+0+2−2 = 0
	y := make([]float64, len(x))
	for i := range x {
		y[i] = 1 + 2*x[i] + e[i]
	}
	y[2] = nan // 任一变量为 NaN 的行不参与、输出 NaN
	out, reason := runCross(t, "cs_resid", [][]float64{y, x}, nil)
	if reason != "" {
		t.Fatalf("reason %q", reason)
	}
	// 去掉第 2 行后残差仍满足 Σe = 0、Σx·e = 0，回归不变。
	expectRow(t, "cs_resid", out, []float64{1, -2, nan, 2, -1, nan})
}

// 奇异与样本不足：整截面为 NaN，报告 cs_resid_singular。
func TestResidualSingular(t *testing.T) {
	cases := []struct {
		name  string
		op    string
		args  [][]float64
		group []float64
	}{
		// 有效行数等于参数个数（截距 + 2 个 x = 3）。
		{"n equals p", "cs_resid", [][]float64{{1, 2, 4, nan}, {1, 2, 3, 4}, {0, 1, 0, 1}}, nil},
		{"duplicate column", "cs_resid", [][]float64{{1, 3, 2, 5, 4}, {1, 2, 3, 4, 5}, {2, 4, 6, 8, 10}}, nil},
		{"constant column", "cs_resid", [][]float64{{1, 3, 2, 5, 4}, {7, 7, 7, 7, 7}}, nil},
		// x 在每组内为常数：与哑变量共线。
		{"constant within groups", "gs_resid", [][]float64{{1, 3, 2, 5, 4, 6}, {1, 1, 1, 2, 2, 2}}, []float64{0, 0, 0, 1, 1, 1}},
		// 两组、一个 x：参数 3 个，有效行 3 个。
		{"grouped n equals p", "gs_resid", [][]float64{{1, 3, 2, 5}, {1, 2, 3, nan}}, []float64{0, 0, 1, 1}},
	}
	for _, c := range cases {
		out, reason := runCross(t, c.op, c.args, c.group)
		if reason != ReasonResidSingular {
			t.Errorf("%s: reason %q, want %q", c.name, reason, ReasonResidSingular)
		}
		for i, v := range out {
			if !math.IsNaN(v) {
				t.Errorf("%s: out[%d] = %v, want NaN", c.name, i, v)
			}
		}
	}
	// 没有有效行：全为 NaN，但不算奇异（与其他截面函数没有有效样本时一致）。
	out, reason := runCross(t, "cs_resid", [][]float64{{nan, nan}, {1, 2}}, nil)
	if reason != "" || !math.IsNaN(out[0]) || !math.IsNaN(out[1]) {
		t.Fatalf("empty section: %v %q", out, reason)
	}
	// 样本数比参数个数多一个：可以算。
	if _, reason := runCross(t, "cs_resid", [][]float64{{1, 2, 4, 3}, {1, 2, 3, 4}, {0, 1, 0, 1}}, nil); reason != "" {
		t.Fatalf("n = p + 1 rejected: %q", reason)
	}
}

// 近似共线：x 与截距（或分组哑变量）只差 1 ulp 时判为奇异，与 numpy matrix_rank 的判定一致（rank 1、rank 2）；
// 扰动大得多（1、1e−3）时 numpy 判满秩，这里也照常算。期望的秩由 numpy 2.5 算出。
func TestResidualNearlyCollinearMatchesNumpyRank(t *testing.T) {
	y := []float64{1, 3, 2, 5, 4, 7, 6, 8}
	ulp := math.Nextafter(1e6, 2e6)
	g := []float64{0, 0, 0, 0, 1, 1, 1, 1}
	perturbed := func(delta1, delta6 float64) []float64 {
		x := []float64{5, 5, 5, 5, 9, 9, 9, 9}
		x[1], x[6] = delta1, delta6
		return x
	}
	cases := []struct {
		name     string
		op       string
		args     [][]float64
		group    []float64
		singular bool
	}{
		{"ulp 常数列", "cs_resid", [][]float64{y, {1e6, ulp, 1e6, ulp, 1e6, ulp, 1e6, ulp}}, nil, true},
		{"相差 1 的列", "cs_resid", [][]float64{y, {1e6, 1e6 + 1, 1e6, 1e6 + 1, 1e6, 1e6 + 1, 1e6, 1e6 + 1}}, nil, false},
		{"组内常数加 ulp", "gs_resid", [][]float64{y, perturbed(math.Nextafter(5, 6), math.Nextafter(9, 10))}, g, true},
		{"组内常数加 1e-3", "gs_resid", [][]float64{y, perturbed(5+1e-3, 9)}, g, false},
	}
	for _, c := range cases {
		out, reason := runCross(t, c.op, c.args, c.group)
		if c.singular {
			if reason != ReasonResidSingular {
				t.Errorf("%s: reason %q, want singular (out %v)", c.name, reason, out)
			}
			continue
		}
		if reason != "" || math.IsNaN(out[0]) {
			t.Errorf("%s: reason %q out %v, want regular", c.name, reason, out)
		}
	}
}

// gs_resid 不带 x 时与 gs_demean 逐位相同（每组至少 2 个样本，不触发样本不足）。
func TestGroupResidualWithoutRegressorsIsDemean(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 200; trial++ {
		n := 6 + rng.Intn(20)
		y, g := make([]float64, n), make([]float64, n)
		for i := range y {
			y[i] = rng.NormFloat64() * 100
			g[i] = float64(i % 3)
			if rng.Float64() < 0.1 && i >= 6 {
				y[i] = nan
			}
			if rng.Float64() < 0.1 && i >= 6 {
				g[i] = nan
			}
		}
		resid, _ := runCross(t, "gs_resid", [][]float64{y}, g)
		demean, _ := runCross(t, "gs_demean", [][]float64{y}, g)
		for i := range y {
			if math.Float64bits(resid[i]) != math.Float64bits(demean[i]) && !(math.IsNaN(resid[i]) && math.IsNaN(demean[i])) {
				t.Fatalf("trial %d row %d: gs_resid %v, gs_demean %v", trial, i, resid[i], demean[i])
			}
		}
	}
}

// 组内去均值法与加分组哑变量的完整设计矩阵结果一致（Frisch–Waugh–Lovell）；cs_resid 等于只有一组的 gs_resid。
func TestGroupResidualMatchesDummyRegression(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for trial := 0; trial < 200; trial++ {
		n, k, groups := 12+rng.Intn(30), 1+rng.Intn(4), 1+rng.Intn(4)
		y := make([]float64, n)
		g := make([]float64, n)
		xs := make([][]float64, k)
		for j := range xs {
			xs[j] = make([]float64, n)
		}
		for i := 0; i < n; i++ {
			g[i] = float64(i % groups)
			for j := range xs {
				xs[j][i] = rng.NormFloat64() * float64(j+1)
			}
			y[i] = 3*g[i] + rng.NormFloat64()
			for j := range xs {
				y[i] += float64(j-1) * xs[j][i]
			}
		}
		// 完整设计矩阵：每组一个哑变量 + 各 x，不去均值，直接用同一个 QR 求残差。
		var design [][]float64
		for level := 0; level < groups; level++ {
			dummy := make([]float64, n)
			for i := range dummy {
				if g[i] == float64(level) {
					dummy[i] = 1
				}
			}
			design = append(design, dummy)
		}
		design = append(design, xs...)
		want, ok := leastSquaresResidual(y, design, nil)
		if !ok {
			t.Fatalf("trial %d: reference regression singular", trial)
		}
		got, reason := runCross(t, "gs_resid", append([][]float64{y}, xs...), g)
		if reason != "" {
			t.Fatalf("trial %d: reason %q", trial, reason)
		}
		for i := range y {
			if math.Abs(got[i]-want[i]) > 1e-9*math.Max(1, math.Abs(want[i])) {
				t.Fatalf("trial %d row %d: %v vs dummy regression %v", trial, i, got[i], want[i])
			}
		}
		if groups == 1 {
			cs, _ := runCross(t, "cs_resid", append([][]float64{y}, xs...), nil)
			for i := range y {
				if math.Float64bits(cs[i]) != math.Float64bits(got[i]) {
					t.Fatalf("trial %d row %d: cs_resid %v, gs_resid with one group %v", trial, i, cs[i], got[i])
				}
			}
		}
	}
}

// 残差与每个自变量、截距正交（最小二乘的正规方程）。
func TestResidualOrthogonality(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	n := 40
	y, x1, x2 := make([]float64, n), make([]float64, n), make([]float64, n)
	for i := range y {
		x1[i], x2[i] = rng.NormFloat64()*1e3, rng.NormFloat64()
		y[i] = 5 + 0.002*x1[i] - 3*x2[i] + rng.NormFloat64()
	}
	out, _ := runCross(t, "cs_resid", [][]float64{y, x1, x2}, nil)
	for name, column := range map[string][]float64{"one": nil, "x1": x1, "x2": x2} {
		sum, scale := 0.0, 0.0
		for i := range out {
			v := 1.0
			if column != nil {
				v = column[i]
			}
			sum += out[i] * v
			scale += math.Abs(out[i] * v)
		}
		if math.Abs(sum) > 1e-10*scale {
			t.Errorf("residual not orthogonal to %s: %v (scale %v)", name, sum, scale)
		}
	}
}
