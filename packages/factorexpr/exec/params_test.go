package exec

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

func buildProgram(t *testing.T, inputs ...factorexpr.ProgramInput) *factorexpr.Program {
	t.Helper()
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func compileFactor(t *testing.T, text string) *factorexpr.Factor {
	t.Helper()
	factor, err := factorexpr.Compile(text)
	if err != nil {
		t.Fatal(err)
	}
	return factor
}

func constant(count int, value float64) []float64 {
	vector := make([]float64, count)
	for i := range vector {
		vector[i] = value
	}
	return vector
}

// 按标的覆盖的窗口：每条序列的结果与“该序列单独用对应的常量窗口”逐位相同（同一套内核）。
func TestPerSeriesWindowMatchesConstantWindow(t *testing.T) {
	panel := purityPanel(t, 6, 700, 7)
	param := compileFactor(t, "params: {w: {type: int, min: 2, max: 40, default: 10}}\noutputs:\n  o: ts_std(x, w) + ema(x, w)\n")
	program := buildProgram(t, factorexpr.ProgramInput{Key: "m", FactorID: "f", Factor: param})
	windows := []float64{2, 10, 40, 10, 25, 3}
	result, err := Run(context.Background(), program, panel, Options{Start: 600, End: 699, Workers: 3,
		Params: map[string][]float64{factorexpr.ParamSlot("m", "w"): windows}})
	if err != nil {
		t.Fatal(err)
	}
	for s, window := range windows {
		fixed := compileFactor(t, "outputs:\n  o: ts_std(x, "+formatInt(window)+") + ema(x, "+formatInt(window)+")\n")
		expected, err := Run(context.Background(), buildProgram(t, factorexpr.ProgramInput{FactorID: "f", Factor: fixed}), panel,
			Options{Start: 600, End: 699})
		if err != nil {
			t.Fatal(err)
		}
		for i := 600; i <= 699; i++ {
			if !sameBits(result.Value(0, s, i), expected.Value(0, s, i)) {
				t.Fatalf("series %d window %v at %d: %v vs %v", s, window, i, result.Value(0, s, i), expected.Value(0, s, i))
			}
		}
	}
}

func formatInt(value float64) string { return strconv.Itoa(int(value)) }

// 目标期的快照覆盖整个回看区间：ts_mean(x * k, 2) 在某一期用这一期的 k 计算窗口里的两根，不用历史各期的 k。
// 补算同一段时只要每期参数相同（一个参数段），结果与逐期实时计算逐位相同，与分块边界无关。
func TestSnapshotCoversLookbackAndMatchesAcrossChunks(t *testing.T) {
	panel := purityPanel(t, 4, 120, 11)
	factor := compileFactor(t, "params: {k: {type: float, min: 0, max: 5, default: 1}}\noutputs:\n  o: ts_mean(x * k, 2) + delay(y, 1) * k\n")
	program := buildProgram(t, factorexpr.ProgramInput{Key: "m", Factor: factor})
	params := map[string][]float64{factorexpr.ParamSlot("m", "k"): {2, 0.5, 3, 1}}
	whole, err := Run(context.Background(), program, panel, Options{Start: 50, End: 119, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range [][2]int{{50, 60}, {61, 61}, {62, 119}} {
		part, err := Run(context.Background(), program, panel, Options{Start: chunk[0], End: chunk[1], Params: params})
		if err != nil {
			t.Fatal(err)
		}
		for s := 0; s < 4; s++ {
			for i := chunk[0]; i <= chunk[1]; i++ {
				if !sameBits(whole.Value(0, s, i), part.Value(0, s, i)) {
					t.Fatalf("series %d at %d: %v vs %v", s, i, whole.Value(0, s, i), part.Value(0, s, i))
				}
			}
		}
	}
	// 直接验算一个点：两根都乘以本期的 k。
	s, i, k := 1, 80, 0.5
	x0, x1, y := panel.Value(0, s, i-1), panel.Value(0, s, i), panel.Value(1, s, i-1)
	want := (x0*k+x1*k)/2 + y*k
	if got := whole.Value(0, s, i); !math.IsNaN(want) && math.Abs(got-want) > 1e-12 {
		t.Fatalf("value = %v, want %v", got, want)
	}
}

// 两个成员参数取值相同时只算一次；取值不同时各自计算；不含参数的子树始终共享。
func TestRuntimeDedupByParamValues(t *testing.T) {
	panel := purityPanel(t, 4, 80, 3)
	factor := compileFactor(t, "params: {w: {type: int, min: 2, max: 20, default: 5}}\noutputs:\n  o: ts_mean(abs(x), w)\n")
	program := buildProgram(t,
		factorexpr.ProgramInput{Key: "a", Factor: factor},
		factorexpr.ProgramInput{Key: "b", Factor: factor})
	same, err := Run(context.Background(), program, panel, Options{Start: 60, End: 79})
	if err != nil {
		t.Fatal(err)
	}
	if same.DedupedNodes == 0 {
		t.Fatalf("identical defaults should dedup")
	}
	different, err := Run(context.Background(), program, panel, Options{Start: 60, End: 79,
		Params: map[string][]float64{factorexpr.ParamSlot("b", "w"): constant(4, 7)}})
	if err != nil {
		t.Fatal(err)
	}
	if different.DedupedNodes >= same.DedupedNodes {
		t.Fatalf("deduped = %d with different params, %d with same", different.DedupedNodes, same.DedupedNodes)
	}
	for s := 0; s < 4; s++ {
		for i := 60; i <= 79; i++ {
			if !sameBits(same.Value(0, s, i), same.Value(1, s, i)) {
				t.Fatalf("same params differ at %d/%d", s, i)
			}
		}
	}
	differs := false
	for s := 0; s < 4; s++ {
		for i := 60; i <= 79; i++ {
			differs = differs || !sameBits(different.Value(0, s, i), different.Value(1, s, i))
		}
	}
	if !differs {
		t.Fatalf("different params should differ")
	}
}

// 截面算子只在成员集合的标的范围内计算：范围外为 NaN，范围内的名次只在范围内排。
func TestCrossSectionRestrictedToUniverse(t *testing.T) {
	keys := []SeriesKey{{Subject: "A"}, {Subject: "B"}, {Subject: "C"}}
	panel, err := NewPanel(1, []string{"x"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	for s, value := range []float64{3, 1, 2} {
		panel.Set(0, s, 0, value)
		panel.MarkPresent(s, 0)
	}
	factor := compileFactor(t, "outputs:\n  r: cs_rank(x)\n")
	program := buildProgram(t,
		factorexpr.ProgramInput{Key: "all", Factor: factor, Universe: "all"},
		factorexpr.ProgramInput{Key: "ab", Factor: factor, Universe: "ab"})
	result, err := Run(context.Background(), program, panel, Options{Start: 0, End: 0,
		Universes: map[string][]bool{"ab": {true, true, false}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := []float64{result.Value(0, 0, 0), result.Value(0, 1, 0), result.Value(0, 2, 0)}; got[0] != 1 || got[1] != 1.0/3 || got[2] != 2.0/3 {
		t.Fatalf("all = %v", got)
	}
	if got := []float64{result.Value(1, 0, 0), result.Value(1, 1, 0)}; got[0] != 1 || got[1] != 0.5 || !math.IsNaN(result.Value(1, 2, 0)) {
		t.Fatalf("ab = %v, %v", got, result.Value(1, 2, 0))
	}
}

// 取值不满足算子联合约束的序列（提交时本应被拒绝）结果为 NaN，不影响其他序列。
func TestInvalidJointParamsYieldNaN(t *testing.T) {
	panel, err := NewPanel(800, []string{"x"}, []SeriesKey{{Subject: "A"}, {Subject: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	for s := 0; s < 2; s++ {
		for i := 0; i < 800; i++ {
			panel.Set(0, s, i, float64(i%17))
		}
	}
	factor := compileFactor(t, "params:\n  n: {type: int, min: 2, max: 20, default: 10}\n  m: {type: int, min: 1, max: 19, default: 1}\noutputs:\n  s: sma(x, n, m)\n")
	program := buildProgram(t, factorexpr.ProgramInput{Key: "m", Factor: factor})
	result, err := Run(context.Background(), program, panel, Options{Start: 799, End: 799,
		Params: map[string][]float64{factorexpr.ParamSlot("m", "n"): {10, 5}, factorexpr.ParamSlot("m", "m"): {1, 5}}})
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(result.Value(0, 1, 799)) {
		t.Fatalf("invalid params should yield NaN, got %v", result.Value(0, 1, 799))
	}
	if math.IsNaN(result.Value(0, 0, 799)) {
		t.Fatalf("valid series should compute")
	}
}

func TestRunRejectsWrongParamLength(t *testing.T) {
	panel := purityPanel(t, 2, 50, 1)
	factor := compileFactor(t, "params: {w: {type: int, min: 2, max: 5, default: 3}}\noutputs:\n  o: ts_mean(x, w)\n")
	program := buildProgram(t, factorexpr.ProgramInput{Key: "m", Factor: factor})
	if _, err := Run(context.Background(), program, panel, Options{Start: 40, End: 49,
		Params: map[string][]float64{factorexpr.ParamSlot("m", "w"): {3}}}); err == nil {
		t.Fatal("wrong length should fail")
	}
}
