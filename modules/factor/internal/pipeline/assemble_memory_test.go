package pipeline

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// 组装阶段的峰值内存（单 tag、补算）：2000 标的 × 200 期 × 10 个时序成员，共 400 万项。要几 GB 内存、跑十几秒，
// 不进 CI：设 MOOX_ASSEMBLE_MEMORY=1 才跑（-short 时也跳过）。输出组装期间堆的峰值（扣掉组装前的基线）与累计分配。
// 2026-11-14 实测（Apple M4）：改成只按序列之前（按标的分支）峰值 2.83～2.96 GB、累计 4.01 GB；按序列另建（项、tag）表时
// 3.75～3.87 GB、5.48 GB；逐项结果改为从按项的 tag 位图推出后 2.89～2.94 GB、4.01 GB，回到原来的水平。
func TestAssembleSingleTagRecalcPeakMemory(t *testing.T) {
	if testing.Short() || os.Getenv("MOOX_ASSEMBLE_MEMORY") != "1" {
		t.Skip("设 MOOX_ASSEMBLE_MEMORY=1 才跑组装内存实测")
	}
	const subjects, periods, members = 2000, 200, 10
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	ids := make([]string, subjects)
	for i := range ids {
		ids[i] = fmt.Sprintf("S%04d", i)
	}
	defs := make([]domain.FactorDef, members)
	for i := range defs {
		defs[i] = testPipelineFactor(fmt.Sprintf("m%02d", i), domain.FactorTypeTimeSeries, 3, false)
	}
	plan := Plan{
		Mode: ModeRecalc, Set: domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "bars", Freq: "1m"},
		Members: domain.AsMembers(defs...), TargetStart: start, TargetEnd: start.Add(periods * time.Minute),
		Expected: ids, Available: ids, ExpectedSeries: singleSeries(ids...),
	}
	loaded := LoadResult{Available: ids, AvailableSeries: plan.ExpectedSeries, Frames: make(map[string]*storageio.Frame, subjects)}
	computation := Computation{Results: map[string]map[string]ItemResult{}, FactorStates: map[string]storageio.FactorState{}}
	for _, subject := range ids {
		frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
		for p := 0; p < periods; p++ {
			frame.Rows = append(frame.Rows, []any{start.Add(time.Duration(p) * time.Minute), singleTag, float64(p)})
		}
		loaded.Frames[subject] = frame
	}
	for _, member := range plan.Members {
		bySubject := make(map[string]ItemResult, subjects)
		for _, subject := range ids {
			item := ItemResult{Member: member.Key, Columns: append([]string{"data_time", "series_tag"}, member.OutputColumns()...)}
			for p := 0; p < periods; p++ {
				item.Rows = append(item.Rows, []any{start.Add(time.Duration(p) * time.Minute), singleTag, float64(p)})
			}
			bySubject[subject] = item
		}
		computation.Results[member.Key] = bySubject
		computation.FactorStates[member.Key] = storageio.FactorState{MemberKey: member.Key, Status: "complete"}
	}
	runner := NewRunner(&fakeStore{}, nil, periodclock.Continuous{}, Config{})

	for round := 0; round < 3; round++ {
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		var peak atomic.Uint64
		done := make(chan struct{})
		sampled := make(chan struct{})
		go func() {
			defer close(sampled)
			var stats runtime.MemStats
			for {
				runtime.ReadMemStats(&stats)
				if stats.HeapAlloc > peak.Load() {
					peak.Store(stats.HeapAlloc)
				}
				select {
				case <-done:
					return
				case <-time.After(2 * time.Millisecond):
				}
			}
		}()
		rows, report, err := runner.AssembleWithReport(plan, loaded, computation)
		close(done)
		<-sampled
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		require.NoError(t, err)
		require.Len(t, rows, subjects*periods)
		require.Len(t, report.Items, subjects*periods*members)
		for _, status := range report.Items {
			require.Equal(t, ItemWritten, status)
		}
		t.Logf("第 %d 轮：组装峰值堆（扣基线）%.2f GB，累计分配 %.2f GB", round+1,
			float64(peak.Load()-before.HeapAlloc)/1e9, float64(after.TotalAlloc-before.TotalAlloc)/1e9)
	}
}
