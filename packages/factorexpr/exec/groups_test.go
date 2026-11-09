package exec

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// groupPanel 是一个时刻、两个 series_tag 的面板，列 x、y 与分组键 g：
//
//	venue:a  A(x=1, y=2, g=1)  B(x=3, y=NaN, g=1)  C(x=5, y=1, g=2)  D(x=2, y=4, g=NaN)  E(没有源行，各列 NaN)
//	venue:b  A(x=10, y=1, g=1) F(x=20, y=3, g=1)  G(x=40, y=2, g=1)
func groupPanel(t *testing.T) *Panel {
	t.Helper()
	keys := []SeriesKey{{"A", "venue:a"}, {"B", "venue:a"}, {"C", "venue:a"}, {"D", "venue:a"}, {"E", "venue:a"},
		{"A", "venue:b"}, {"F", "venue:b"}, {"G", "venue:b"}}
	panel, err := NewPanel(1, []string{"x", "y", "g"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	nan := math.NaN()
	rows := [][3]float64{{1, 2, 1}, {3, nan, 1}, {5, 1, 2}, {2, 4, nan}, {nan, nan, nan}, {10, 1, 1}, {20, 3, 1}, {40, 2, 1}}
	for i, key := range keys {
		s, _ := panel.SeriesIndex(key)
		for c, v := range rows[i] {
			panel.Set(c, s, 0, v)
		}
		if key.Subject != "E" {
			panel.MarkPresent(s, 0)
		}
	}
	return panel
}

func expectCells(t *testing.T, panel *Panel, result *Result, column int, tag string, want map[string]float64) {
	t.Helper()
	for subject, expected := range want {
		if got := valueOf(t, panel, result, column, subject, tag); !sameBits(got, expected) && math.Abs(got-expected) > 1e-12 {
			t.Errorf("%s %s/%s = %v, want %v", result.Columns[column].Name, subject, tag, got, expected)
		}
	}
}

// 双输入：cs_corr 只用两者都有效的行，结果广播到截面每一行（含参数之一为 NaN 的 B 与没有源行的 E）；
// 分组键：gs_mean 写给同组的每一行，g 为 NaN 的 D 与没有源行的 E（g 也是 NaN）为 NaN；cs_mean 则写给 E。
func TestCrossMultipleInputsAndGroupKey(t *testing.T) {
	panel := groupPanel(t)
	program := compileProgram(t, map[string]string{
		"corr": "cs_corr(x, y)", "gmean": "gs_mean(x, g)", "cmean": "cs_mean(x)", "grank": "gs_rank(x, g)",
		"wmean": "gs_wmean(x, y, g)", "resid": "cs_resid(y, x)",
	})
	result := run(t, program, panel, 0, 0, 1)
	nan := math.NaN()
	// venue:a 两者都有效：A(1,2) C(5,1) D(2,4)。
	mx, my := 8.0/3, 7.0/3
	sxy := (1-mx)*(2-my) + (5-mx)*(1-my) + (2-mx)*(4-my)
	sxx := (1-mx)*(1-mx) + (5-mx)*(5-mx) + (2-mx)*(2-mx)
	syy := (2-my)*(2-my) + (1-my)*(1-my) + (4-my)*(4-my)
	corr := sxy / math.Sqrt(sxx*syy)
	expectCells(t, panel, result, columnByName(result, "corr"), "venue:a", map[string]float64{"A": corr, "B": corr, "C": corr, "D": corr, "E": corr})
	expectCells(t, panel, result, columnByName(result, "gmean"), "venue:a", map[string]float64{"A": 2, "B": 2, "C": 5, "D": nan, "E": nan})
	expectCells(t, panel, result, columnByName(result, "cmean"), "venue:a", map[string]float64{"E": 11.0 / 4, "D": 11.0 / 4})
	expectCells(t, panel, result, columnByName(result, "grank"), "venue:a", map[string]float64{"A": 0.5, "B": 1, "C": 1, "D": nan, "E": nan})
	// 加权均值：组 1 里 B 的权重 y 为 NaN，不参与；A 的值 1、权重 2。
	expectCells(t, panel, result, columnByName(result, "wmean"), "venue:a", map[string]float64{"A": 1, "B": 1, "C": 5, "D": nan})
	expectCells(t, panel, result, columnByName(result, "gmean"), "venue:b", map[string]float64{"A": 70.0 / 3, "F": 70.0 / 3, "G": 70.0 / 3})
	// cs_resid：参数之一为 NaN 的行输出 NaN。
	expectCells(t, panel, result, columnByName(result, "resid"), "venue:a", map[string]float64{"B": nan, "E": nan})
	if got := valueOf(t, panel, result, columnByName(result, "resid"), "A", "venue:a"); math.IsNaN(got) {
		t.Fatal("cs_resid on a valid row should not be NaN")
	}
}

// 多输入截面同样只在成员标的范围内计算，范围外为 NaN。
func TestCrossMultipleInputsRespectUniverse(t *testing.T) {
	panel := groupPanel(t)
	factor, err := factorexpr.Compile("output: gs_sum(x, g)\n")
	if err != nil {
		t.Fatal(err)
	}
	program, err := factorexpr.NewProgram([]factorexpr.ProgramInput{{FactorID: "s", Factor: factor, Universe: "u"}})
	if err != nil {
		t.Fatal(err)
	}
	mask := make([]bool, len(panel.Series()))
	for i, key := range panel.Series() {
		mask[i] = key.Subject != "B"
	}
	result, err := Run(context.Background(), program, panel, Options{Start: 0, End: 0, Universes: map[string][]bool{"u": mask}})
	if err != nil {
		t.Fatal(err)
	}
	expectCells(t, panel, result, 0, "venue:a", map[string]float64{"A": 1, "B": math.NaN(), "C": 5})
}

// 告警：奇异截面计入依赖它的每个成员，同一成员的多个输出共用一个节点时只计一次；只统计目标区间内的截面；与工作线程数无关。
func TestCrossWarningsPerMember(t *testing.T) {
	const length = 40
	keys := []SeriesKey{{"A", "t"}, {"B", "t"}, {"C", "t"}, {"D", "t"}}
	panel, err := NewPanel(length, []string{"x", "y"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	for s := range keys {
		for tm := 0; tm < length; tm++ {
			x := float64(s*s + tm%5)
			if tm%4 == 0 {
				x = 7 // 每 4 根有一个截面 x 为常数：与截距共线
			}
			panel.Set(0, s, tm, x)
			panel.Set(1, s, tm, float64(s+tm))
			panel.MarkPresent(s, tm)
		}
	}
	multi, err := factorexpr.Compile("outputs:\n  r1: cs_resid(y, x)\n  r2: cs_resid(y, x) * 2\n")
	if err != nil {
		t.Fatal(err)
	}
	single, err := factorexpr.Compile("output: ts_mean(cs_resid(y, x), 3)\n")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := factorexpr.Compile("output: cs_rank(x)\n")
	if err != nil {
		t.Fatal(err)
	}
	program, err := factorexpr.NewProgram([]factorexpr.ProgramInput{
		{Key: "m", FactorID: "m", Factor: multi}, {Key: "s", FactorID: "s", Factor: single}, {Key: "p", FactorID: "p", Factor: plain}})
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 8} {
		result, err := Run(context.Background(), program, panel, Options{Start: 10, End: 29, Workers: workers})
		if err != nil {
			t.Fatal(err)
		}
		// 目标区间 [10, 29] 内 x 为常数的截面：12、16、20、24、28，共 5 个；ts_mean 向前需要的 8、9 两根不计。
		want := map[string]map[string]int{"m": {"cs_resid_singular": 5}, "s": {"cs_resid_singular": 5}}
		if len(result.Warnings) != len(want) || result.Warnings["m"]["cs_resid_singular"] != 5 || result.Warnings["s"]["cs_resid_singular"] != 5 {
			t.Fatalf("workers=%d warnings = %v, want %v", workers, result.Warnings, want)
		}
	}
	// 大面板真正走并行：各分块的计数合并后与单线程完全相同（-race 覆盖合并处的锁）。
	big := purityPanel(t, 64, 300, 5)
	for s := range big.Series() {
		for tm := 0; tm < 300; tm += 4 {
			big.Set(0, s, tm, 7)
		}
	}
	const start, end = 20, 299
	one, err := Run(context.Background(), program, big, Options{Start: start, End: end, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := Run(context.Background(), program, big, Options{Start: start, End: end, Workers: 8})
	if err != nil {
		t.Fatal(err)
	}
	if got := one.Warnings["m"]["cs_resid_singular"]; got != 140 {
		t.Fatalf("single-thread warnings = %v, want 140（70 个时刻 × 2 个 series_tag）", one.Warnings)
	}
	if fmt.Sprint(one.Warnings) != fmt.Sprint(parallel.Warnings) {
		t.Fatalf("warnings depend on workers: %v vs %v", one.Warnings, parallel.Warnings)
	}
	result, err := Run(context.Background(), program, panel, Options{Start: 13, End: 15})
	if err != nil {
		t.Fatal(err)
	}
	if result.Warnings != nil {
		t.Fatalf("no singular section in [13, 15], got %v", result.Warnings)
	}
}
