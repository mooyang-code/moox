package engine

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorexpr"
)

func TestLoadLibraryMatchesCatalog(t *testing.T) {
	library, err := LoadLibrary("../../factors")
	if err != nil {
		t.Fatal(err)
	}
	if len(library) != 11 {
		t.Fatalf("library has %d factors", len(library))
	}
	if got := library["ZscoreAbsMeanQ"]; got.Lookback != 58 || got.OutputNames()[0] != "zscore_abs_mean_q_20" {
		t.Fatalf("ZscoreAbsMeanQ = %v", got)
	}
	if _, err := LoadLibrary(t.TempDir()); err == nil {
		t.Fatal("empty library directory should be rejected")
	}
}

// 用构造的窗口与“基线结果”核对比较逻辑：一致的值、行式滚动跨过空洞（新引擎为 null，原因 gap）、
// 新上市头部（head）、旧为 null 新有值、基线缺行。
func TestCompareChunkClassifies(t *testing.T) {
	library, err := LoadLibrary("../../factors")
	if err != nil {
		t.Fatal(err)
	}
	set := domain.FactorSet{SetID: "fset_x_1m", SpaceID: "crypto", SourceDatasetID: "dataset_x", Freq: "1m"}
	def := domain.FactorDef{FactorID: "Bias", Outputs: []string{"bias_20"}, LookbackPeriods: 20}
	target, err := newCompareTarget(set, []domain.FactorDef{def}, library, 2)
	if err != nil {
		t.Fatal(err)
	}
	clock := periodclock.Continuous{}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	const length = 80 // 网格下标 0..79；目标从下标 19 开始
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: set, TargetStart: base.Add(19 * time.Minute), TargetEnd: base.Add(length * time.Minute)}
	closes := func(subject string, i int) float64 { return 100 + float64(i) + float64(len(subject)) }
	frames := map[string]*storageio.Frame{}
	results := map[string]pipeline.ItemResult{}
	for _, subject := range []string{"BTC", "ETH", "NEW"} {
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		var rows [][]any
		var window []float64
		for i := 0; i < length; i++ {
			if subject == "ETH" && i == 50 {
				continue // 空洞：旧实现按行滚动跨过它，新引擎在网格上给 null
			}
			if subject == "NEW" && i < 30 {
				continue // 新上市：头部没有数据
			}
			at := base.Add(time.Duration(i) * time.Minute)
			value := closes(subject, i)
			frame.Rows = append(frame.Rows, []any{at, "binance", value})
			window = append(window, value)
			if len(window) > 20 {
				window = window[1:]
			}
			if i < 19 {
				continue
			}
			sum := 0.0
			for _, v := range window {
				sum += v
			}
			old := any(value / (sum / float64(len(window))))
			if subject == "BTC" && i == 70 {
				old = nil // 旧为 null，新有值
			}
			row := []any{at.Format(time.RFC3339), "binance", old}
			if subject == "BTC" && i == 75 {
				continue // 基线缺这一行
			}
			rows = append(rows, row)
		}
		frames[subject] = frame
		results[subject] = pipeline.ItemResult{FactorID: "Bias", Columns: []string{"data_time", "series_tag", "bias_20"}, Rows: rows}
	}
	computation := pipeline.Computation{Results: map[string]map[string]pipeline.ItemResult{"Bias": results}}
	if err := target.compareChunk(context.Background(), clock, plan, pipeline.LoadResult{Frames: frames}, computation); err != nil {
		t.Fatal(err)
	}
	stats := target.stats["bias_20"]
	if stats.OldNullNewValue != 1 || stats.MissingBaseline != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	// 覆盖缺失那根的窗口是 t = 50..69；t = 50 本身没有源行、不参与比较，所以是 19 个。
	if stats.OldValueNewNull[ReasonGap] != 19 {
		t.Fatalf("gap = %d, want 19 (windows covering the missing bar)", stats.OldValueNewNull[ReasonGap])
	}
	if stats.OldValueNewNull[ReasonHead] != 19 {
		t.Fatalf("head = %d, want 19", stats.OldValueNewNull[ReasonHead])
	}
	if stats.OldValueNewNull[ReasonComplete] != 0 || stats.MaxRelError > 1e-12 || stats.BothValue == 0 {
		t.Fatalf("stats = %+v", stats)
	}
	failures := CompareReport{Outputs: target.outputStats()}.Failures()
	if len(failures) != 1 || !strings.Contains(failures[0], "旧为 null、新有值") {
		t.Fatalf("failures = %v", failures)
	}
}

func TestCompareReportFailures(t *testing.T) {
	report := CompareReport{Outputs: []*OutputStats{
		{Set: "s", Output: "a", MaxRelError: 5e-3, ContinuousOutliers: map[string]int64{OutlierVerified: 3, OutlierCancellation: 2}},
		{Set: "s", Output: "b", ContinuousOutliers: map[string]int64{OutlierUnexplained: 1}},
		{Set: "s", Output: "c", Discrete: true, DiscreteFlips: 10, TieFlips: 10},
		{Set: "s", Output: "d", Discrete: true, DiscreteFlips: 11, TieFlips: 10, UnexplainedFlips: 1},
	}}
	failures := report.Failures()
	if len(failures) != 2 || !strings.Contains(failures[0], "s/b") || !strings.Contains(failures[1], "s/d") {
		t.Fatalf("failures = %v", failures)
	}
	if !math.IsNaN(numeric("x")) || numeric(int64(3)) != 3 {
		t.Fatal("numeric conversion")
	}
}

// 裁决：排名差异在新实现的窗口里有精确并列时判为并列翻转；标准差差异用 256 位重算核对；接近 0 的抵消单独归类。
func TestCompareAdjudication(t *testing.T) {
	library := map[string]*factorexpr.Factor{}
	for id, spec := range map[string]string{
		"R": "outputs:\n  r: ts_rank(x, 3)\n",
		"S": "outputs:\n  s: ts_std(x, 3)\n",
		"D": "outputs:\n  d: x - ts_mean(x, 3)\n",
	} {
		factor, err := factorexpr.Compile(spec)
		if err != nil {
			t.Fatal(err)
		}
		library[id] = factor
	}
	set := domain.FactorSet{SetID: "s", Freq: "1m"}
	defs := []domain.FactorDef{
		{FactorID: "R", Outputs: []string{"r"}, LookbackPeriods: 3},
		{FactorID: "S", Outputs: []string{"s"}, LookbackPeriods: 3},
		{FactorID: "D", Outputs: []string{"d"}, LookbackPeriods: 3},
	}
	target, err := newCompareTarget(set, defs, library, 1)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// x: 1, 2, 2, 3, 1e6；t=2 窗口 [1, 2, 2] 当期值 2 有并列；t=3 窗口 [2, 2, 3] 当期值 3 没有并列。
	values := []float64{1, 2, 2, 3, 1e6}
	frame := &storageio.Frame{SubjectID: "A", Columns: []string{"data_time", "series_tag", "x"}}
	for i, v := range values {
		frame.Rows = append(frame.Rows, []any{base.Add(time.Duration(i) * time.Minute), "", v})
	}
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: set, TargetStart: base.Add(2 * time.Minute), TargetEnd: base.Add(5 * time.Minute)}
	row := func(i int, v any) []any {
		return []any{base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339), "", v}
	}
	stdAt2 := 0.5773502691896258 // [1, 2, 2] 的样本标准差
	computation := pipeline.Computation{Results: map[string]map[string]pipeline.ItemResult{
		"R": {"A": {Columns: []string{"data_time", "series_tag", "r"}, Rows: [][]any{row(2, 0.5), row(3, 0.5), row(4, 1.0)}}},
		"S": {"A": {Columns: []string{"data_time", "series_tag", "s"}, Rows: [][]any{row(2, stdAt2*(1+1e-6)), row(3, 0.0), row(4, 577349.1)}}},
		"D": {"A": {Columns: []string{"data_time", "series_tag", "d"}, Rows: [][]any{row(2, 0.33), row(3, 1.0), row(4, 666665.0)}}},
	}}
	if err := target.compareChunk(context.Background(), periodclock.Continuous{}, plan, pipeline.LoadResult{Frames: map[string]*storageio.Frame{"A": frame}}, computation); err != nil {
		t.Fatal(err)
	}
	r, sd, d := target.stats["r"], target.stats["s"], target.stats["d"]
	// r：t=2 新值 (1 + (2+1)/2)/3 ≈ 0.833，旧 0.5，窗口 [1, 2, 2] 当期值有并列 → 并列翻转；
	// t=3 新值 1，旧 0.5，窗口 [2, 2, 3] 当期值没有并列，但 256 位重算的排名就是 1 → 判为 verified。
	if r.TieFlips != 1 || r.VerifiedFlips != 1 || r.UnexplainedFlips != 0 {
		t.Fatalf("rank stats = %+v", r)
	}
	// 三个“旧值”都偏离了真值，新值与 256 位重算一致：全部判为 verified_reference。
	if sd.ContinuousOutliers[OutlierVerified] != 3 {
		t.Fatalf("std stats = %+v", sd)
	}
	// d 的前两个旧值是编造的错值（第三个 666665 恰好正确），新值与高精度重算一致。
	if d.ContinuousOutliers[OutlierVerified] != 2 || d.ContinuousOutliers[OutlierUnexplained] != 0 {
		t.Fatalf("diff stats = %+v", d)
	}
}
