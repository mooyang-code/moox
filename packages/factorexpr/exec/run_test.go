package exec

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
)

// purityExpressions 覆盖全部算子，外加几个嵌套与共享的写法。
var purityExpressions = []string{
	"ts_sum(x, 7)", "ts_mean(x, 7)", "ts_min(x, 7)", "ts_max(x, 7)", "ts_product(x / 100, 5)",
	"ts_std(x, 7)", "ts_var(x, 7)", "ts_zscore(x, 7)", "ts_rank(x, 7)", "ts_quantile(x, 9, 0.3)",
	"ts_median(x, 8)", "ts_skew(x, 9)", "ts_kurt(x, 9)", "ts_argmax(x, 7)", "ts_argmin(x, 7)",
	"ts_corr(x, y, 9)", "ts_cov(x, y, 9)", "ts_decay_linear(x, 7)", "ts_count(x > y, 7)",
	"ts_slope(x, 9)", "ts_rsquare(x, 9)", "ts_resi(x, 9)", "ema(x, 20)", "sma(x, 10, 1)",
	"delay(x, 3)", "delta(x, 2)", "pct_change(x, 2)", "cs_rank(x)", "cs_zscore(x)", "cs_demean(x)", "cs_scale(x)",
	"where(ema(x, 20) > 0, 1, 0)", "ts_rank(ts_mean(x, 5) - y, 10)", "cs_rank(ts_mean(x, 5))",
	"where(y > 0, x / y, 0)", "abs(x) + sqrt(abs(y)) * log(abs(x) + 1)",
}

// purityPanel 生成多序列、两个 series_tag 的面板：含 NaN、常数段，第一条序列放入 Codex 审查给出的 EMA 反例。
func purityPanel(t *testing.T, seriesCount, length int, seed int64) *Panel {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	var keys []SeriesKey
	for i := 0; i < seriesCount; i++ {
		keys = append(keys, SeriesKey{Subject: fmt.Sprintf("S%03d", i), Tag: []string{"venue:a", "venue:b"}[i%2]})
	}
	panel, err := NewPanel(length, []string{"x", "y"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	for s := 0; s < seriesCount; s++ {
		level := 100.0
		for i := 0; i < length; i++ {
			level *= math.Exp(rng.NormFloat64() * 0.01)
			x, y := level-100, rng.NormFloat64()
			if s == 0 {
				switch {
				case i == 0:
					x = -1
				case i == 1:
					x = 1
				case i <= 208:
					x = 0
				}
			}
			if i >= 250 && i < 270 && s%3 == 1 {
				x = 5 // 常数段
			}
			if rng.Float64() < 0.01 && !(s == 0 && i <= 208) {
				x = math.NaN()
			}
			panel.Set(0, s, i, x)
			panel.Set(1, s, i, y)
			if rng.Float64() < 0.98 {
				panel.MarkPresent(s, i)
			}
		}
	}
	return panel
}

func run(t *testing.T, program *factorexpr.Program, panel *Panel, start, end, workers int) *Result {
	t.Helper()
	result, err := Run(context.Background(), program, panel, Options{Start: start, End: end, Workers: workers})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func sameBits(a, b float64) bool {
	return math.Float64bits(a) == math.Float64bits(b) || (math.IsNaN(a) && math.IsNaN(b))
}

func expressionSet(extra func(i int, expression string) map[string]string) map[string]string {
	expressions := map[string]string{}
	for i, expression := range purityExpressions {
		expressions[fmt.Sprintf("e%02d", i)] = expression
		if extra != nil {
			for name, value := range extra(i, expression) {
				expressions[name] = value
			}
		}
	}
	return expressions
}

func columnByName(result *Result, name string) int {
	for i, column := range result.Columns {
		if column.Name == name {
			return i
		}
	}
	return -1
}

// 点纯性（设计 5.1）：同一个点在单独计算、与其他因子共享节点（需求区间被拉宽）、实时只算一行、补算分块等上下文中逐位相同。
func TestPointPurityAcrossContexts(t *testing.T) {
	const length = 420
	panel := purityPanel(t, 6, length, 1)
	shared := compileProgram(t, expressionSet(func(i int, expression string) map[string]string {
		// 再引用一次并位移，让共享节点的需求区间向前延伸。
		return map[string]string{fmt.Sprintf("d%02d", i): "delay(" + expression + ", 5)"}
	}))
	sharedResult := run(t, shared, panel, shared.Lookback-1, length-1, 1)
	for i, expression := range purityExpressions {
		name := fmt.Sprintf("e%02d", i)
		alone := compileProgram(t, map[string]string{name: expression})
		start := alone.Lookback - 1
		baseline := run(t, alone, panel, start, length-1, 1)
		check := func(context string, other *Result, column int) {
			for s := range panel.series {
				for tm := max(baseline.Start, other.Start); tm <= min(baseline.End, other.End); tm++ {
					if a, b := baseline.Value(0, s, tm), other.Value(column, s, tm); !sameBits(a, b) {
						t.Fatalf("%s [%s] series %d t=%d: %v vs %v", expression, context, s, tm, a, b)
					}
				}
			}
		}
		check("shared", sharedResult, columnByName(sharedResult, name))
		for _, tm := range []int{start, start + 1, length/2 + 3, length - 1} {
			check(fmt.Sprintf("live t=%d", tm), run(t, alone, panel, tm, tm, 1), 0)
		}
		middle := (start + length - 1) / 2
		check("chunk 1", run(t, alone, panel, start, middle, 1), 0)
		check("chunk 2", run(t, alone, panel, middle+1, length-1, 1), 0)
	}
}

// Codex 审查给出的反例：x[0]=−1, x[1]=1, x[2..208]=0。where(ema(x,20) > 0, 1, 0) 单独计算与和
// delay(ema(x,20), 1) 一起计算时结果必须相同。截断 EMA 在 t=208 时只看 [1, 208]，值为正。
func TestEMACounterexample(t *testing.T) {
	panel := purityPanel(t, 2, 300, 2)
	alone := compileProgram(t, map[string]string{"a": "where(ema(x, 20) > 0, 1, 0)"})
	both := compileProgram(t, map[string]string{"a": "where(ema(x, 20) > 0, 1, 0)", "b": "delay(ema(x, 20), 1)"})
	first := run(t, alone, panel, 207, 299, 1)
	second := run(t, both, panel, 208, 299, 1)
	column := columnByName(second, "a")
	for tm := 208; tm <= 299; tm++ {
		if !sameBits(first.Value(0, 0, tm), second.Value(column, 0, tm)) {
			t.Fatalf("t=%d: %v vs %v", tm, first.Value(0, 0, tm), second.Value(column, 0, tm))
		}
	}
	if first.Value(0, 0, 208) != 1 {
		t.Fatalf("where(ema > 0) at t=208 = %v, want 1", first.Value(0, 0, 208))
	}
}

// 确定性：结果与工作线程数无关（面板足够大，走并行路径）。
func TestDeterministicAcrossWorkers(t *testing.T) {
	const length = 300
	panel := purityPanel(t, 64, length, 3)
	program := compileProgram(t, expressionSet(nil))
	baseline := run(t, program, panel, program.Lookback-1, length-1, 1)
	for _, workers := range []int{2, 8} {
		other := run(t, program, panel, program.Lookback-1, length-1, workers)
		for c := range baseline.Columns {
			for i, v := range baseline.Columns[c].Values {
				if !sameBits(v, other.Columns[c].Values[i]) {
					t.Fatalf("workers=%d column %s index %d: %v vs %v", workers, baseline.Columns[c].Name, i, v, other.Columns[c].Values[i])
				}
			}
		}
	}
}

// 内存估算与执行时实际的缓冲区峰值完全一致（设计 7.5）。
func TestMemoryEstimateMatchesExecution(t *testing.T) {
	const length = 300
	panel := purityPanel(t, 8, length, 4)
	program := compileProgram(t, expressionSet(nil))
	for _, target := range [][2]int{{program.Lookback - 1, length - 1}, {length - 1, length - 1}, {250, 260}} {
		estimate, err := EstimateMemory(program, len(panel.series), length, target[0], target[1])
		if err != nil {
			t.Fatal(err)
		}
		result := run(t, program, panel, target[0], target[1], 4)
		if estimate.PeakBuffers != result.PeakBufferBytes {
			t.Fatalf("target %v: estimate %d, actual %d", target, estimate.PeakBuffers, result.PeakBufferBytes)
		}
		if estimate.Panel <= 0 || estimate.Result <= 0 {
			t.Fatalf("estimate = %+v", estimate)
		}
	}
}

// 实时周期：共享的长窗口节点 + 大量不同输出时，输出缓冲只占“序列数 × 1”（设计 7.2）。
func TestLiveLongWindowUsesSmallBuffers(t *testing.T) {
	const length, seriesCount = 1440, 50
	expressions := map[string]string{}
	for i := 0; i < 300; i++ {
		expressions[fmt.Sprintf("o%03d", i)] = fmt.Sprintf("ts_mean(x, 1440) + %d", i)
	}
	program := compileProgram(t, expressions)
	panel := purityPanel(t, seriesCount, length, 5)
	result := run(t, program, panel, length-1, length-1, 1)
	if limit := int64(301 * seriesCount * 8); result.PeakBufferBytes > limit {
		t.Fatalf("peak buffers = %d bytes, want at most %d", result.PeakBufferBytes, limit)
	}
}

func TestResultPresenceAndShape(t *testing.T) {
	panel := purityPanel(t, 4, 50, 6)
	program := compileProgram(t, map[string]string{"m": "ts_mean(x, 5)"})
	result := run(t, program, panel, 10, 19, 1)
	if result.Len() != 10 || len(result.Series) != 4 || len(result.Columns[0].Values) != 40 {
		t.Fatalf("shape: len %d, series %d, values %d", result.Len(), len(result.Series), len(result.Columns[0].Values))
	}
	for s := range result.Series {
		for tm := 10; tm <= 19; tm++ {
			if result.Present(s, tm) != panel.present[s*panel.length+tm] {
				t.Fatalf("presence mismatch at series %d t=%d", s, tm)
			}
		}
	}
}

func TestRunErrors(t *testing.T) {
	panel := purityPanel(t, 2, 30, 7)
	program := compileProgram(t, map[string]string{"m": "ts_mean(x, 10)"})
	if _, err := Run(context.Background(), program, panel, Options{Start: 5, End: 29}); err == nil || !strings.Contains(err.Error(), "网格不足") {
		t.Fatalf("short grid: err = %v", err)
	}
	if _, err := Run(context.Background(), program, panel, Options{Start: 9, End: 30}); err == nil || !strings.Contains(err.Error(), "网格不足") {
		t.Fatalf("target beyond grid: err = %v", err)
	}
	missing := compileProgram(t, map[string]string{"m": "ts_mean(volume, 3)"})
	if _, err := Run(context.Background(), missing, panel, Options{Start: 2, End: 29}); err == nil || !strings.Contains(err.Error(), "缺少输入列 volume") {
		t.Fatalf("missing column: err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, program, panel, Options{Start: 9, End: 29}); err == nil {
		t.Fatal("canceled context should stop the run")
	}
	if _, err := NewPanel(5, []string{"x"}, []SeriesKey{{"A", "t"}, {"A", "t"}}); err == nil {
		t.Fatal("duplicate series should be rejected")
	}
	if _, err := NewPanel(0, []string{"x"}, nil); err == nil {
		t.Fatal("empty grid should be rejected")
	}
}

// 截面算子只在同一个 series_tag 内计算，输入为 NaN 的标的被排除。
func TestCrossSectionGroupsByTag(t *testing.T) {
	keys := []SeriesKey{{"A", "venue:a"}, {"B", "venue:a"}, {"C", "venue:b"}, {"D", "venue:b"}, {"E", "venue:b"}}
	panel, err := NewPanel(1, []string{"x"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range []float64{1, 2, 30, math.NaN(), 10} {
		s, _ := panel.SeriesIndex(keys[i])
		panel.Set(0, s, 0, v)
	}
	program := compileProgram(t, map[string]string{"r": "cs_rank(x)"})
	result := run(t, program, panel, 0, 0, 1)
	want := map[string]float64{"A": 0.5, "B": 1, "C": 1, "D": math.NaN(), "E": 0.5}
	for i, key := range result.Series {
		if got := result.Value(0, i, 0); !sameBits(got, want[key.Subject]) {
			t.Errorf("%s: cs_rank = %v, want %v", key.Subject, got, want[key.Subject])
		}
	}
}
