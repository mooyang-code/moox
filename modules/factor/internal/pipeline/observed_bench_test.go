package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// observedBenchLoad 造 subjects 个本期可用的标的与 history 个已下架的标的，各 bars 根 K 线（下架的标的没有目标期的行）。
func observedBenchLoad(b *testing.B, target time.Time, subjects, history, bars int) (LoadResult, []string, []string) {
	b.Helper()
	frames := make(map[string]*storageio.Frame, subjects+history)
	var available, departed []string
	for s := 0; s < subjects+history; s++ {
		subject := fmt.Sprintf("S%04d", s)
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		last := 0
		if s >= subjects {
			last = 1 + s%7 // 下架于目标期之前的几根
			departed = append(departed, subject)
		} else {
			available = append(available, subject)
		}
		for i := bars - 1; i >= last; i-- {
			at := target.Add(-time.Duration(i) * time.Minute)
			frame.Rows = append(frame.Rows, []any{at, "venue:binance", 100 + float64((s*7919+i*104729)%1000)/10})
		}
		frames[subject] = frame
	}
	view, err := viewFromFrames(frames, 60)
	if err != nil {
		b.Fatal(err)
	}
	return LoadResult{Frames: map[string]*storageio.Frame{}, View: view, Available: available}, available, departed
}

// 500 个标的、回看 1000：current 与 observed（另加 100 个已下架标的的历史）一期实时计算的耗时与分配。
// go test -run '^$' -bench BenchmarkObservedCompute -benchmem ./internal/pipeline/
func BenchmarkObservedCompute(b *testing.B) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	loaded, available, departed := observedBenchLoad(b, target, 500, 100, 1000)
	for _, mode := range []string{"current", "observed"} {
		b.Run(mode, func(b *testing.B) {
			spec := "output: ts_mean(cs_rank(close), 1000)\n"
			if mode == "observed" {
				spec = "cross_section_history: observed\n" + spec
			}
			def := domain.MustDefinition("bench", "bench", spec)
			def.AllowPartialUniverse = true
			plan := computePlan(target, []domain.FactorDef{def}, available)
			if mode == "observed" {
				plan.HistorySubjects = departed
			}
			runner := newComputeRunner(Config{ComputeWorkers: 4})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				computation, err := runner.Compute(context.Background(), plan, loaded)
				if err != nil || computation.FactorStates["bench"].Status != "complete" {
					b.Fatalf("compute: %v %v", err, computation.FactorStates["bench"])
				}
			}
			b.StopTimer()
			estimate, err := EstimateChunkMemory(plan.Members, len(available)+len(plan.HistorySubjects), 1, 1)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(estimate.Total())/(1<<20), "est-MiB")
		})
	}
}
