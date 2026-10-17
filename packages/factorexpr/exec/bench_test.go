package exec

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"gopkg.in/yaml.v3"
)

// 基准对应设计 7.7 的性能指标：500 个标的的 1m 集合。

func ohlcvPanel(b *testing.B, seriesCount, length int) *Panel {
	b.Helper()
	rng := rand.New(rand.NewSource(1))
	keys := make([]SeriesKey, seriesCount)
	for i := range keys {
		keys[i] = SeriesKey{Subject: fmt.Sprintf("S%04d", i), Tag: "binance"}
	}
	columns := []string{"open", "high", "low", "close", "volume", "quote_volume"}
	panel, err := NewPanel(length, columns, keys)
	if err != nil {
		b.Fatal(err)
	}
	for s := 0; s < seriesCount; s++ {
		price := 100.0
		for i := 0; i < length; i++ {
			open := price
			price *= math.Exp(rng.NormFloat64() * 0.003)
			volume := 100 + rng.Float64()*900
			values := []float64{open, math.Max(open, price) * 1.001, math.Min(open, price) * 0.999, price, volume, volume * price}
			for c, v := range values {
				panel.Set(c, s, i, v)
			}
			panel.MarkPresent(s, i)
		}
	}
	return panel
}

func libraryProgram(b *testing.B) *factorexpr.Program {
	b.Helper()
	raw, err := os.ReadFile("../testdata/xbx_factors.yaml")
	if err != nil {
		b.Fatal(err)
	}
	var entries []libraryEntry
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		b.Fatal(err)
	}
	var inputs []factorexpr.ProgramInput
	for _, entry := range entries {
		factor, err := factorexpr.Compile(entry.Spec)
		if err != nil {
			b.Fatal(err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: entry.FactorID, Factor: factor})
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		b.Fatal(err)
	}
	return program
}

// syntheticProgram 参照 Alpha101 的结构生成 count 个因子，窗口在 5～60 之间变化。
func syntheticProgram(b *testing.B, count int) *factorexpr.Program {
	b.Helper()
	templates := []string{
		"ts_rank(close / ts_mean(close, {w}) - 1, {w})",
		"ts_corr(close, volume, {w})",
		"ts_std(pct_change(close), {w})",
		"(close - ts_min(low, {w})) / (ts_max(high, {w}) - ts_min(low, {w}))",
		"ts_zscore(volume, {w})",
		"ema(close, {w}) / close",
		"ts_decay_linear(pct_change(close), {w})",
		"cs_rank(ts_mean(volume, {w}))",
		"ts_skew(pct_change(close), {w})",
		"ts_slope(close, {w}) / close",
		"ts_quantile(volume, {w}, 0.8)",
		"ts_argmax(high, {w})",
	}
	var inputs []factorexpr.ProgramInput
	for i := 0; i < count; i++ {
		window := 5 + (i*7)%56
		expression := strings.ReplaceAll(templates[i%len(templates)], "{w}", fmt.Sprint(window))
		factor, err := factorexpr.Compile(fmt.Sprintf("outputs:\n  o%03d: %s\n", i, expression))
		if err != nil {
			b.Fatal(err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: fmt.Sprintf("f%03d", i), Factor: factor})
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		b.Fatal(err)
	}
	return program
}

func benchmarkRun(b *testing.B, program *factorexpr.Program, panel *Panel, start, end, workers int) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Run(context.Background(), program, panel, Options{Start: start, End: end, Workers: workers}); err != nil {
			b.Fatal(err)
		}
	}
}

// 实时一期：500 标的 × 现有 11 个因子（目标 ≤ 20ms）。
func BenchmarkLive11(b *testing.B) {
	program := libraryProgram(b)
	panel := ohlcvPanel(b, 500, program.Lookback)
	for _, workers := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			benchmarkRun(b, program, panel, program.Lookback-1, program.Lookback-1, workers)
		})
	}
}

// 补算一个分块：500 期 × 500 标的 × 11 个因子（目标 ≤ 2s）。
func BenchmarkRecalcChunk11(b *testing.B) {
	program := libraryProgram(b)
	length := program.Lookback - 1 + 500
	panel := ohlcvPanel(b, 500, length)
	for _, workers := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			benchmarkRun(b, program, panel, program.Lookback-1, length-1, workers)
		})
	}
}

// 实时一期：500 标的 × 合成的 300 个因子（目标 ≤ 500ms）。
func BenchmarkLive300(b *testing.B) {
	program := syntheticProgram(b, 300)
	panel := ohlcvPanel(b, 500, program.Lookback)
	for _, workers := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			benchmarkRun(b, program, panel, program.Lookback-1, program.Lookback-1, workers)
		})
	}
}

// 实时一期：共享的 1440 窗口均值 + 300 个不同输出。
func BenchmarkLiveLongWindow300(b *testing.B) {
	var inputs []factorexpr.ProgramInput
	for i := 0; i < 300; i++ {
		factor, err := factorexpr.Compile(fmt.Sprintf("outputs:\n  o%03d: ts_mean(close, 1440) + %d\n", i, i))
		if err != nil {
			b.Fatal(err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: fmt.Sprintf("f%03d", i), Factor: factor})
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		b.Fatal(err)
	}
	panel := ohlcvPanel(b, 500, 1440)
	benchmarkRun(b, program, panel, 1439, 1439, 1)
}
