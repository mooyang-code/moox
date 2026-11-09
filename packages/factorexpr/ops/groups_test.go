package ops

import (
	"math"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

func runCross(t *testing.T, op string, args [][]float64, group []float64) ([]float64, string) {
	t.Helper()
	kernel := CrossKernel(op, nil, "")
	if kernel == nil {
		t.Fatalf("%s has no cross kernel", op)
	}
	n := len(args[0])
	subjects := make([]string, n)
	for i := range subjects {
		subjects[i] = string(rune('A' + i))
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = 12345 // 内核必须写满每一行
	}
	reason := kernel(Section{Args: args, Group: group, Subjects: subjects}, out)
	return out, reason
}

func expectRow(t *testing.T, label string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len %d, want %d", label, len(got), len(want))
	}
	for i := range want {
		if !sameValue(got[i], want[i]) && math.Abs(got[i]-want[i]) > 1e-12 {
			t.Errorf("%s[%d] = %v, want %v (all %v)", label, i, got[i], want[i], got)
		}
	}
}

// 全部截面函数都有内核（执行器靠它找内核，漏登记会在运行时才报错）。
func TestEveryCrossFunctionHasKernel(t *testing.T) {
	for _, fn := range factorexpr.Functions() {
		if fn.Category != factorexpr.CategoryCrossSection {
			continue
		}
		if CrossKernel(fn.Name, []float64{0.5}, "A") == nil {
			t.Errorf("%s has no cross kernel", fn.Name)
		}
		if _, ok := grouped[fn.Name]; ok != (fn.GroupArg() >= 0) {
			t.Errorf("%s: grouped kernel %v, group argument %d", fn.Name, ok, fn.GroupArg())
		}
	}
}

// cs_corr：只用两者都有效的行；有效对数少于 3 或任一方为常数时为 NaN，广播到每一行。
func TestCrossCorrelationByHand(t *testing.T) {
	x := []float64{1, 2, 3, nan, 5}
	y := []float64{2, 4, 7, 1, nan}
	// 有效对 (1,2) (2,4) (3,7)：r = Σdxdy / sqrt(Σdx² Σdy²) = (1·(−2.333)+0+1·2.667) / sqrt(2 · 12.667)
	want := (-1*(2-13.0/3) + 1*(7-13.0/3)) / math.Sqrt(2*((2-13.0/3)*(2-13.0/3)+(4-13.0/3)*(4-13.0/3)+(7-13.0/3)*(7-13.0/3)))
	out, _ := runCross(t, "cs_corr", [][]float64{x, y}, nil)
	expectRow(t, "cs_corr", out, []float64{want, want, want, want, want})
	out, _ = runCross(t, "cs_corr", [][]float64{{1, 2, nan}, {1, 2, 3}}, nil)
	expectRow(t, "cs_corr two pairs", out, []float64{nan, nan, nan})
	out, _ = runCross(t, "cs_corr", [][]float64{{1, 1, 1, 1}, {1, 2, 3, 4}}, nil)
	expectRow(t, "cs_corr constant", out, []float64{nan, nan, nan, nan})
	out, _ = runCross(t, "cs_corr", [][]float64{{1, 2, 3}, {3, 2, 1}}, nil)
	expectRow(t, "cs_corr perfect", out, []float64{-1, -1, -1})
}

// 分组截面的手算用例：两组、g 为 NaN 的行、组内全 NaN、单样本组、并列。
func TestGroupedKernelsByHand(t *testing.T) {
	//            A  B    C  D    E  F    G
	g := []float64{1, 1, nan, 2, 2, 1, -0.0}
	x := []float64{3, 1, 9, nan, nan, 3, 5}
	cases := []struct {
		op   string
		args [][]float64
		want []float64
	}{
		// 组 1 = {A, B, F}，组 2 = {D, E}（x 全 NaN），组 0 = {G}；C 不属于任何组。
		{"gs_rank", [][]float64{x}, []float64{5.0 / 6, 1.0 / 3, nan, nan, nan, 5.0 / 6, 1}},
		{"gs_demean", [][]float64{x}, []float64{3 - 7.0/3, 1 - 7.0/3, nan, nan, nan, 3 - 7.0/3, 0}},
		{"gs_zscore", [][]float64{x}, []float64{(3 - 7.0/3) / math.Sqrt(4.0/3), (1 - 7.0/3) / math.Sqrt(4.0/3), nan, nan, nan, (3 - 7.0/3) / math.Sqrt(4.0/3), nan}},
		// 聚合广播：写给组内每一行（含 x 为 NaN 的行），组内没有有效样本为 NaN；g 为 NaN 的行为 NaN。
		{"gs_mean", [][]float64{x}, []float64{7.0 / 3, 7.0 / 3, nan, nan, nan, 7.0 / 3, 5}},
		{"gs_sum", [][]float64{x}, []float64{7, 7, nan, nan, nan, 7, 5}},
		{"gs_median", [][]float64{x}, []float64{3, 3, nan, nan, nan, 3, 5}},
		{"gs_count", [][]float64{{1, 0, 1, 1, nan, 1, nan}}, []float64{2, 2, nan, 1, 1, 2, nan}},
		// 加权：w ≤ 0 或 NaN 的行不参与。组 1：(3·2 + 1·0(不参与) + 3·NaN(不参与)) / 2 = 3。
		{"gs_wmean", [][]float64{x, {2, 0, 1, 1, 1, nan, -1}}, []float64{3, 3, nan, nan, nan, 3, nan}},
		// 组间：组 1 的值 7/3，组 0 的值 5，组 2 没有值。
		{"gx_rank", [][]float64{x}, []float64{0.5, 0.5, nan, nan, nan, 0.5, 1}},
		{"gx_zscore", [][]float64{x}, []float64{-1 / math.Sqrt(2), -1 / math.Sqrt(2), nan, nan, nan, -1 / math.Sqrt(2), 1 / math.Sqrt(2)}},
	}
	for _, c := range cases {
		out, reason := runCross(t, c.op, c.args, g)
		if reason != "" {
			t.Fatalf("%s reason %q", c.op, reason)
		}
		expectRow(t, c.op, out, c.want)
	}
}

// gs_cumshare：组内按 x 降序累计占比，并列按标的顺序；排在最后的一行恰好为 1；组内总和为 0 时为 NaN。
func TestGroupCumulativeShare(t *testing.T) {
	g := []float64{1, 1, 1, 1, 2, 2, nan}
	x := []float64{2, 5, 2, 1, 0, 0, 4}
	out, _ := runCross(t, "gs_cumshare", [][]float64{x}, g)
	// 组 1 降序：B(5) A(2) C(2) D(1)，总和 10。
	expectRow(t, "gs_cumshare", out, []float64{0.7, 0.5, 0.9, 1, nan, nan, nan})
	if out[3] != 1 {
		t.Fatalf("last share = %v, want exactly 1", out[3])
	}
}

// 组数上限：超过 MaxGroups 个不同取值时整个截面为 NaN，并报告原因；恰好 MaxGroups 个时照常计算。
func TestTooManyGroups(t *testing.T) {
	for _, count := range []int{MaxGroups, MaxGroups + 1} {
		g := make([]float64, count)
		x := make([]float64, count)
		for i := range g {
			g[i], x[i] = float64(i), float64(i)
		}
		for _, op := range []string{"gs_rank", "gs_mean", "gx_rank", "gs_resid"} {
			args := [][]float64{x}
			if op == "gs_resid" {
				args = [][]float64{x, x}
			}
			out, reason := runCross(t, op, args, g)
			if count > MaxGroups {
				if reason != ReasonTooManyGroups {
					t.Fatalf("%s with %d groups: reason %q", op, count, reason)
				}
				for i := range out {
					if !math.IsNaN(out[i]) {
						t.Fatalf("%s with %d groups: out[%d] = %v, want NaN", op, count, i, out[i])
					}
				}
				continue
			}
			if reason == ReasonTooManyGroups {
				t.Fatalf("%s with %d groups rejected", op, count)
			}
		}
	}
}

// g 全为 NaN：没有任何组，结果全为 NaN，不报告告警。
func TestGroupKeyAllNaN(t *testing.T) {
	for _, op := range []string{"gs_rank", "gs_mean", "gs_cumshare", "gx_zscore", "gs_resid"} {
		out, reason := runCross(t, op, [][]float64{{1, 2, 3}}, []float64{nan, nan, nan})
		if reason != "" {
			t.Fatalf("%s reason %q", op, reason)
		}
		expectRow(t, op, out, []float64{nan, nan, nan})
	}
}
