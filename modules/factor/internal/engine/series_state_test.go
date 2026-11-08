package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

const (
	engineSpotTag = "venue:binance|market:spot|source:spot_http"
	engineSwapTag = "venue:binance|market:swap|source:swap_http"
)

func engineSeries(subject, tag string) seriesset.Series {
	return seriesset.Series{SubjectID: subject, SeriesTag: tag}
}

// 缺口恢复：截面因子按 marker 里冻结的序列全集重算（统一数据集），选择里带按序列的全集。
func TestGapFillerCrossSectionUsesFrozenSeriesUniverse(t *testing.T) {
	factor := gapFactor("rank", domain.FactorTypeCrossSection, false)
	universe := []seriesset.Series{
		engineSeries("BTC", engineSpotTag), engineSeries("BTC", engineSwapTag),
		engineSeries("ETH", engineSpotTag), engineSeries("ETH", engineSwapTag),
		engineSeries("SOL", engineSpotTag),
	}
	f := newGapFixture(t, fakeUniverse{subjects: []string{"BTC", "ETH", "SOL"}, series: universe, found: true}, factor)
	f.record(t, gapT0, "ETH", 0, "evt-1", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 1)
	selection := f.chunks.calls[0].selection
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, selection.Subjects)
	require.Equal(t, universe, selection.Series, "SOL 只有现货：冻结全集里没有它的永续，不会被当成缺数")
}

// 缺口补齐后改写摘要：补齐的标的从按序列的失败名单里去掉；跳过的 series_tag 要等没有失败的标的了才清除；
// 只跳过一部分 series_tag 的成员，补算没写成的标的仍然缺结果。
func TestRunTrackerRepairUpdatesSeriesLists(t *testing.T) {
	tracker := newRunTracker()
	period := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	axis, err := periodclock.AxisFor("crypto", "1m")
	require.NoError(t, err)
	outcome := pipeline.Outcome{
		Status: "degraded", FailedSubjects: []string{"ETH", "SOL"},
		Factors: []storageio.FactorState{{
			MemberKey: "rank", FactorID: "rank", MemberKind: "fixed", Status: "degraded",
			FailedSubjects:    []string{"ETH", "SOL"},
			FailedSeries:      []seriesset.Series{engineSeries("ETH", engineSwapTag), engineSeries("SOL", engineSwapTag)},
			SkippedSeriesTags: []string{engineSwapTag},
		}},
	}
	tracker.record("t1", axis, period, "degraded", outcome, period.Add(time.Minute))
	run, _ := tracker.lastRecorded("t1")
	require.Equal(t, []string{engineSwapTag}, run.factors[0].SkippedSeriesTags)
	require.Len(t, run.factors[0].FailedSeries, 2)

	// ETH 补齐，SOL 这次没写成：SOL 仍然失败，永续仍然跳过。
	tracker.repair("t1", RepairedPeriod{
		Period: period, Done: true,
		Repaired:  []pipeline.ItemKey{{Member: "rank", Subject: "ETH", Period: period}},
		Unwritten: []pipeline.ItemKey{{Member: "rank", Subject: "SOL", Period: period}},
	}, period.Add(2*time.Minute))
	run, _ = tracker.lastRecorded("t1")
	require.Equal(t, []string{"SOL"}, run.factors[0].FailedSubjects)
	require.Equal(t, []seriesset.Series{engineSeries("SOL", engineSwapTag)}, run.factors[0].FailedSeries)
	require.Equal(t, []string{engineSwapTag}, run.factors[0].SkippedSeriesTags)
	require.Equal(t, "degraded", run.factors[0].Status)

	// SOL 也补齐：失败名单清空、跳过的 tag 清除、成员记为完成。
	tracker.repair("t1", RepairedPeriod{
		Period: period, Done: true,
		Repaired: []pipeline.ItemKey{{Member: "rank", Subject: "SOL", Period: period}},
	}, period.Add(3*time.Minute))
	run, _ = tracker.lastRecorded("t1")
	require.Empty(t, run.factors[0].FailedSubjects)
	require.Empty(t, run.factors[0].FailedSeries)
	require.Empty(t, run.factors[0].SkippedSeriesTags)
	require.Equal(t, "complete", run.factors[0].Status)
}

// 预热完成判断：统一数据集上截面因子只跳过一部分 series_tag（degraded）算已完成；空洞与整体失败仍然不算；单序列任务的判断不变。
func TestRunConfirmsWarmupAcceptsPartialSeriesSkip(t *testing.T) {
	member := domain.AsMembers(domain.MustDefinition("rank", "rank", "output: cs_rank(close)\n"))[0]
	compiled, err := domain.CompileDefinition(member.Factor)
	require.NoError(t, err)
	state := func(status string, mutate func(*domain.FactorPeriodState)) trackedRun {
		factor := domain.FactorPeriodState{
			MemberKey: member.Key, MemberKind: member.Kind, FactorID: member.Factor.FactorID, Status: status,
			DefinitionHash: domain.DefinitionHash(compiled),
		}
		if mutate != nil {
			mutate(&factor)
		}
		return trackedRun{status: "degraded", rows: 10, factors: []domain.FactorPeriodState{factor}, failedSubjects: []string{"SOL"}}
	}
	members := []domain.MemberDef{member}

	partial := state("degraded", func(f *domain.FactorPeriodState) {
		f.FailedSubjects = []string{"SOL"}
		f.FailedSeries = []seriesset.Series{engineSeries("SOL", engineSwapTag)}
		f.SkippedSeriesTags = []string{engineSwapTag}
	})
	require.True(t, runConfirmsWarmup(partial, members), "只跳过永续：缺数的序列是行情本身的状况，不说明窗口没载完")

	warming := partial
	warming.warming = 1
	require.False(t, runConfirmsWarmup(warming, members), "预热中不算")
	failed := state("degraded", func(f *domain.FactorPeriodState) { f.FailedSubjects = []string{"SOL"} })
	require.False(t, runConfirmsWarmup(failed, members), "没有跳过 tag 的 degraded（整体失败）不算：单序列任务的判断不变")
	complete := state("complete", nil)
	complete.status, complete.failedSubjects = "complete", nil
	require.True(t, runConfirmsWarmup(complete, members))
}
