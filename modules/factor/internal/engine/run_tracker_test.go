package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

func TestRunTrackerKeepsNewestPeriodAndComputesLag(t *testing.T) {
	tracker := newRunTracker()
	t1 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	completed := t1.Add(time.Minute + 4*time.Second)
	tracker.record("fset_a", t1, "complete", pipeline.Outcome{}, completed)
	tracker.record("fset_a", t1.Add(-time.Minute), "degraded", pipeline.Outcome{}, completed.Add(time.Second))
	tracker.record("fset_b", t1, "degraded", pipeline.Outcome{
		FailedSubjects: []string{"ETHUSDT"},
		Factors: []storageio.FactorState{
			{FactorID: "momentum", Status: "complete", SourceHash: "abc"},
			{FactorID: "rank", Status: "degraded", FailedSubjects: []string{"ETHUSDT"}},
		},
	}, completed)

	summary := tracker.latest("fset_a", t1.Add(30*time.Second))
	require.Equal(t, t1.Unix(), summary.LastPeriodTime)
	require.Equal(t, "complete", summary.LastStatus)
	require.EqualValues(t, 30, summary.LagSeconds)
	// 完成时间是写完最新一期的时刻，较旧的期次不覆盖它。
	require.Equal(t, completed.Unix(), summary.CompletedAt)
	require.Len(t, tracker.all(t1), 2)
	require.Equal(t, "fset_a", tracker.all(t1)[0].SetID)
	require.Zero(t, tracker.latest("missing", t1).LastPeriodTime)

	degraded := tracker.latest("fset_b", t1)
	require.Equal(t, []string{"ETHUSDT"}, degraded.FailedSubjects)
	require.Equal(t, []domain.FactorPeriodState{
		{FactorID: "momentum", Status: "complete", SourceHash: "abc"},
		{FactorID: "rank", Status: "degraded", FailedSubjects: []string{"ETHUSDT"}},
	}, degraded.Factors)
}

// 预热中跳过的最新一期由缺口补算补齐后改写摘要：去掉补齐的失败标的、不再算预热中，余下没有失败时记为完成，完成时刻是补齐的时刻；
// 补齐之前（账本里这一期还有待办）摘要不变，更早的期次不改写，补算没覆盖的失败原样保留。
func TestRunTrackerUpgradesLatestPeriodOnceRepairIsDone(t *testing.T) {
	period := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	live := period.Add(time.Hour + 9*time.Second)
	warmingRun := pipeline.Outcome{
		Status: "degraded", Warming: 2, FailedSubjects: []string{"BTC", "ETH", "XRP"},
		Factors: []storageio.FactorState{
			{FactorID: "ma", Status: "degraded", FailedSubjects: []string{"BTC", "ETH", "XRP"}},
			{FactorID: "rank", Status: "skipped", FailedSubjects: []string{"BTC", "ETH", "XRP"}},
			{FactorID: "vol", Status: "degraded"},
		},
	}
	items := func(subjects ...string) []pipeline.ItemKey {
		var out []pipeline.ItemKey
		for _, factor := range []string{"ma", "rank"} {
			for _, subject := range subjects {
				out = append(out, pipeline.ItemKey{FactorID: factor, Subject: subject, Period: period})
			}
		}
		return out
	}
	tracker := newRunTracker()
	notified := 0
	tracker.setOnRecord(func() { notified++ })
	tracker.record("fset", period, "degraded", warmingRun, live)
	require.Equal(t, 1, notified)

	require.False(t, tracker.repair("fset", RepairedPeriod{Period: period, Repaired: items("BTC")}, live.Add(time.Minute)), "这一期还有待办：先记下，摘要不变")
	require.False(t, tracker.repair("fset", RepairedPeriod{Period: period.Add(-time.Hour), Repaired: items("ETH"), Done: true}, live.Add(time.Minute)), "更早的期次不改写")
	require.EqualValues(t, 2, tracker.latest("fset", live).WarmingSubjects)
	require.Equal(t, 1, notified)

	repaired := live.Add(4 * time.Minute)
	require.True(t, tracker.repair("fset", RepairedPeriod{Period: period, Repaired: items("ETH"), Done: true}, repaired))
	require.Equal(t, 2, notified, "改写后立刻补发心跳")
	summary := tracker.latest("fset", repaired)
	require.Equal(t, period.Unix(), summary.LastPeriodTime)
	require.Equal(t, repaired.Unix(), summary.CompletedAt)
	require.Zero(t, summary.WarmingSubjects)
	require.Equal(t, []string{"XRP"}, summary.FailedSubjects, "XRP 没有待办（上游失败），补算不覆盖")
	require.Equal(t, "degraded", summary.LastStatus)
	require.Equal(t, []domain.FactorPeriodState{
		{FactorID: "ma", Status: "degraded", FailedSubjects: []string{"XRP"}},
		{FactorID: "rank", Status: "skipped", FailedSubjects: []string{"XRP"}},
		{FactorID: "vol", Status: "degraded"},
	}, summary.Factors, "整因子失败（没有列出标的）保持原状")

	// 没有补算之外的失败：补齐后记为完成。
	clean := pipeline.Outcome{
		Status: "degraded", Warming: 2, FailedSubjects: []string{"BTC", "ETH"},
		Factors: []storageio.FactorState{
			{FactorID: "ma", Status: "degraded", FailedSubjects: []string{"BTC", "ETH"}, GapSubjects: []string{"ETH"}},
			{FactorID: "rank", Status: "skipped", GapSubjects: []string{"BTC"}},
		},
	}
	next := period.Add(time.Hour)
	tracker.record("fset", next, "degraded", clean, live.Add(time.Hour))
	require.False(t, tracker.repair("fset", RepairedPeriod{Period: period, Repaired: items("XRP"), Done: true}, repaired), "已经有更新的一期")
	nextItems := []pipeline.ItemKey{
		{FactorID: "ma", Subject: "BTC", Period: next}, {FactorID: "ma", Subject: "ETH", Period: next},
		{FactorID: "rank", Subject: "BTC", Period: next}, {FactorID: "rank", Subject: "ETH", Period: next},
	}
	require.True(t, tracker.repair("fset", RepairedPeriod{Period: next, Repaired: nextItems, Done: true}, repaired.Add(time.Hour)))
	summary = tracker.latest("fset", repaired.Add(time.Hour))
	require.Equal(t, "complete", summary.LastStatus)
	require.Empty(t, summary.FailedSubjects)
	require.Equal(t, []domain.FactorPeriodState{{FactorID: "ma", Status: "complete"}, {FactorID: "rank", Status: "complete"}}, summary.Factors)
	require.False(t, tracker.repair("fset", RepairedPeriod{Period: next, Repaired: nextItems, Done: true}, repaired.Add(2*time.Hour)), "已完成的期次不再改写")
	require.Equal(t, repaired.Add(time.Hour).Unix(), tracker.latest("fset", repaired).CompletedAt)
}

// 严格截面因子因 A 有洞整期跳过（实时路径一行没写）：补算补齐了 A，但 B 这次没写成，这一期仍缺 B，不能记为完成。
func TestRunTrackerKeepsUnwrittenItemsOfSkippedFactor(t *testing.T) {
	period := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	tracker := newRunTracker()
	tracker.record("fset", period, "degraded", pipeline.Outcome{
		Status:  "degraded",
		Factors: []storageio.FactorState{{FactorID: "rank", Status: "skipped", GapSubjects: []string{"A"}}, {FactorID: "ma", Status: "degraded", FailedSubjects: []string{"A"}, GapSubjects: []string{"A"}}},
	}, period.Add(time.Hour))
	at := func(factor, subject string) pipeline.ItemKey {
		return pipeline.ItemKey{FactorID: factor, Subject: subject, Period: period}
	}
	require.True(t, tracker.repair("fset", RepairedPeriod{
		Period: period, Repaired: []pipeline.ItemKey{at("rank", "A"), at("ma", "A")},
		Unwritten: []pipeline.ItemKey{at("rank", "B"), at("ma", "C")}, Done: true,
	}, period.Add(time.Hour+time.Minute)))
	summary := tracker.latest("fset", period)
	require.Equal(t, "degraded", summary.LastStatus)
	require.Equal(t, []domain.FactorPeriodState{
		{FactorID: "rank", Status: "skipped", FailedSubjects: []string{"B"}},
		{FactorID: "ma", Status: "complete"},
	}, summary.Factors, "时序因子 C 的值实时路径已经写过，补算没写成不影响")
}

func TestPeriodFailureReasonClassifiesAbortedPeriods(t *testing.T) {
	require.Equal(t, "storage_unavailable", periodFailureReason(fmt.Errorf("write: %w", storageio.ErrInfra)))
	require.Equal(t, "timeout", periodFailureReason(fmt.Errorf("compute: %w", context.DeadlineExceeded)))
	require.Equal(t, "timeout", periodFailureReason(context.Canceled))
	require.Equal(t, "internal", periodFailureReason(errors.New("assemble failed")))
}

func (t *runTracker) latest(setID string, now time.Time) domain.SetRunSummary {
	if t == nil {
		return domain.SetRunSummary{SetID: setID}
	}
	t.mu.RLock()
	run, ok := t.runs[setID]
	t.mu.RUnlock()
	if !ok {
		return domain.SetRunSummary{SetID: setID}
	}
	return summaryOf(setID, run, now)
}
