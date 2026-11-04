package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// testAxis 是测试用的 crypto 轴（恒等）。
func testAxis(freq string) periodclock.Axis {
	axis, err := periodclock.AxisFor("crypto", freq)
	if err != nil {
		panic(err)
	}
	return axis
}

func TestRunTrackerKeepsNewestPeriodAndComputesLag(t *testing.T) {
	tracker := newRunTracker()
	t1 := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	completed := t1.Add(time.Minute + 4*time.Second)
	tracker.record("ftask_a", testAxis("1m"), t1, "complete", pipeline.Outcome{}, completed)
	tracker.record("ftask_a", testAxis("1m"), t1.Add(-time.Minute), "degraded", pipeline.Outcome{}, completed.Add(time.Second))
	tracker.record("ftask_b", testAxis("1m"), t1, "degraded", pipeline.Outcome{
		FailedSubjects: []string{"ETHUSDT"},
		Factors: []storageio.FactorState{
			{MemberKey: "momentum", FactorID: "momentum", Status: "complete", SourceHash: "abc"},
			{MemberKey: "rank", FactorID: "rank", Status: "degraded", FailedSubjects: []string{"ETHUSDT"}},
		},
	}, completed)

	summary := tracker.latest("ftask_a", t1.Add(30*time.Second))
	require.Equal(t, t1.Unix(), summary.LastPeriodTime)
	require.Equal(t, "complete", summary.LastStatus)
	// 最新已收盘的一期已算完：落后 0 根。
	require.Zero(t, summary.LagSeconds)
	// 下一根 00:11 开盘、00:12 收盘；收盘 30 秒宽限之后还没写出，落后 1 根 = 60 秒。
	require.EqualValues(t, 60, tracker.latest("ftask_a", t1.Add(2*time.Minute+30*time.Second)).LagSeconds)
	// 完成时间是写完最新一期的时刻，较旧的期次不覆盖它。
	require.Equal(t, completed.Unix(), summary.CompletedAt)
	require.Len(t, tracker.all(t1), 2)
	require.Equal(t, "ftask_a", tracker.all(t1)[0].TaskID)
	require.Zero(t, tracker.latest("missing", t1).LastPeriodTime)

	degraded := tracker.latest("ftask_b", t1)
	require.Equal(t, []string{"ETHUSDT"}, degraded.FailedSubjects)
	require.Equal(t, []domain.FactorPeriodState{
		{MemberKey: "momentum", FactorID: "momentum", Status: "complete", SpecHash: "abc"},
		{MemberKey: "rank", FactorID: "rank", Status: "degraded", FailedSubjects: []string{"ETHUSDT"}},
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
			{MemberKey: "ma", FactorID: "ma", Status: "degraded", FailedSubjects: []string{"BTC", "ETH", "XRP"}},
			{MemberKey: "rank", FactorID: "rank", Status: "skipped", FailedSubjects: []string{"BTC", "ETH", "XRP"}},
			{MemberKey: "vol", FactorID: "vol", Status: "degraded"},
		},
	}
	items := func(subjects ...string) []pipeline.ItemKey {
		var out []pipeline.ItemKey
		for _, factor := range []string{"ma", "rank"} {
			for _, subject := range subjects {
				out = append(out, pipeline.ItemKey{Member: factor, Subject: subject, Period: period})
			}
		}
		return out
	}
	tracker := newRunTracker()
	notified := 0
	tracker.setOnRecord(func() { notified++ })
	tracker.record("fset", testAxis("1m"), period, "degraded", warmingRun, live)
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
		{MemberKey: "ma", FactorID: "ma", Status: "degraded", FailedSubjects: []string{"XRP"}},
		{MemberKey: "rank", FactorID: "rank", Status: "skipped", FailedSubjects: []string{"XRP"}},
		{MemberKey: "vol", FactorID: "vol", Status: "degraded"},
	}, summary.Factors, "整因子失败（没有列出标的）保持原状")

	// 没有补算之外的失败：补齐后记为完成。
	clean := pipeline.Outcome{
		Status: "degraded", Warming: 2, FailedSubjects: []string{"BTC", "ETH"},
		Factors: []storageio.FactorState{
			{MemberKey: "ma", FactorID: "ma", Status: "degraded", FailedSubjects: []string{"BTC", "ETH"}, GapSubjects: []string{"ETH"}},
			{MemberKey: "rank", FactorID: "rank", Status: "skipped", GapSubjects: []string{"BTC"}},
		},
	}
	next := period.Add(time.Hour)
	tracker.record("fset", testAxis("1m"), next, "degraded", clean, live.Add(time.Hour))
	require.False(t, tracker.repair("fset", RepairedPeriod{Period: period, Repaired: items("XRP"), Done: true}, repaired), "已经有更新的一期")
	nextItems := []pipeline.ItemKey{
		{Member: "ma", Subject: "BTC", Period: next}, {Member: "ma", Subject: "ETH", Period: next},
		{Member: "rank", Subject: "BTC", Period: next}, {Member: "rank", Subject: "ETH", Period: next},
	}
	require.True(t, tracker.repair("fset", RepairedPeriod{Period: next, Repaired: nextItems, Done: true}, repaired.Add(time.Hour)))
	summary = tracker.latest("fset", repaired.Add(time.Hour))
	require.Equal(t, "complete", summary.LastStatus)
	require.Empty(t, summary.FailedSubjects)
	require.Equal(t, []domain.FactorPeriodState{{MemberKey: "ma", FactorID: "ma", Status: "complete"}, {MemberKey: "rank", FactorID: "rank", Status: "complete"}}, summary.Factors)
	require.False(t, tracker.repair("fset", RepairedPeriod{Period: next, Repaired: nextItems, Done: true}, repaired.Add(2*time.Hour)), "已完成的期次不再改写")
	require.Equal(t, repaired.Add(time.Hour).Unix(), tracker.latest("fset", repaired).CompletedAt)
}

// 严格截面因子因 A 有洞整期跳过（实时路径一行没写）：补算补齐了 A，但 B 这次没写成，这一期仍缺 B，不能记为完成。
func TestRunTrackerKeepsUnwrittenItemsOfSkippedFactor(t *testing.T) {
	period := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	tracker := newRunTracker()
	tracker.record("fset", testAxis("1m"), period, "degraded", pipeline.Outcome{
		Status:  "degraded",
		Factors: []storageio.FactorState{{MemberKey: "rank", FactorID: "rank", Status: "skipped", GapSubjects: []string{"A"}}, {MemberKey: "ma", FactorID: "ma", Status: "degraded", FailedSubjects: []string{"A"}, GapSubjects: []string{"A"}}},
	}, period.Add(time.Hour))
	at := func(factor, subject string) pipeline.ItemKey {
		return pipeline.ItemKey{Member: factor, Subject: subject, Period: period}
	}
	require.True(t, tracker.repair("fset", RepairedPeriod{
		Period: period, Repaired: []pipeline.ItemKey{at("rank", "A"), at("ma", "A")},
		Unwritten: []pipeline.ItemKey{at("rank", "B"), at("ma", "C")}, Done: true,
	}, period.Add(time.Hour+time.Minute)))
	summary := tracker.latest("fset", period)
	require.Equal(t, "degraded", summary.LastStatus)
	require.Equal(t, []domain.FactorPeriodState{
		{MemberKey: "rank", FactorID: "rank", Status: "skipped", FailedSubjects: []string{"B"}},
		{MemberKey: "ma", FactorID: "ma", Status: "complete"},
	}, summary.Factors, "时序因子 C 的值实时路径已经写过，补算没写成不影响")
}

func TestPeriodFailureReasonClassifiesAbortedPeriods(t *testing.T) {
	require.Equal(t, "storage_unavailable", periodFailureReason(fmt.Errorf("write: %w", storageio.ErrInfra)))
	require.Equal(t, "timeout", periodFailureReason(fmt.Errorf("compute: %w", context.DeadlineExceeded)))
	require.Equal(t, "timeout", periodFailureReason(context.Canceled))
	require.Equal(t, "internal", periodFailureReason(errors.New("assemble failed")))
}

func (t *runTracker) latest(taskID string, now time.Time) domain.TaskRunSummary {
	if t == nil {
		return domain.TaskRunSummary{TaskID: taskID}
	}
	t.mu.RLock()
	run, ok := t.runs[taskID]
	t.mu.RUnlock()
	if !ok {
		return domain.TaskRunSummary{TaskID: taskID}
	}
	return summaryOf(taskID, run, now)
}

// 落后根数按交易 K 线算：A 股算完上午最后一根后，午休、收盘后、周末都是 0；收盘 30 秒宽限后结果还没写出是 1。
// lag_seconds = 根数 × 周期，前端（web/src/views/factor/shared/health.ts 的 runBarLag）直接除以周期。
func TestRunTrackerLagCountsTradingBars(t *testing.T) {
	axis, err := periodclock.AxisFor("stockcn", "5m")
	require.NoError(t, err)
	cst := time.FixedZone("CST", 8*3600)
	at := func(value string) time.Time {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", value, cst)
		require.NoError(t, err)
		return parsed.UTC()
	}
	period := func(value string) time.Time {
		x, err := axis.ToAxis(at(value))
		require.NoError(t, err)
		return x
	}
	for _, c := range []struct {
		period, now string
		bars        int64
	}{
		{"2026-10-09 11:25", "2026-10-09 12:30", 0}, // 午休
		{"2026-10-09 14:55", "2026-10-11 10:00", 0}, // 周末
		{"2026-10-09 14:55", "2026-10-09 15:00", 0}, // 刚收盘、已算完
		{"2026-10-09 14:50", "2026-10-09 15:00", 0}, // 最后一根刚收盘，还在宽限内
		{"2026-10-09 14:50", "2026-10-09 15:01", 1}, // 最后一根已收盘、过了宽限还没写出
		{"2026-10-09 14:55", "2026-10-12 09:41", 2}, // 周一开盘后两根没算
		{"2026-09-30 14:55", "2026-10-08 09:36", 1}, // 国庆后第一根已收盘
	} {
		summary := summaryOf("ftask", trackedRun{periodTime: period(c.period), axis: axis, status: "complete"}, at(c.now))
		require.Equal(t, at(c.period).Unix(), summary.LastPeriodTime, "上报真实时间")
		require.Equal(t, c.bars*300, summary.LagSeconds, "%s @ %s", c.period, c.now)
	}
	// crypto：1m、1h 也能还原。
	for _, freq := range []string{"1m", "5m", "1h"} {
		crypto := testAxis(freq)
		last := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
		step := crypto.Step()
		for bars := int64(0); bars < 3; bars++ {
			now := last.Add(time.Duration(bars+1)*step + step/2) // 过了收盘宽限
			summary := summaryOf("ftask", trackedRun{periodTime: last, axis: crypto}, now)
			require.Equal(t, bars*int64(step/time.Second), summary.LagSeconds, "%s %d", freq, bars)
		}
	}
}

// 当前时刻超出交易日历的覆盖范围时算不出交易 K 线根数：不能显示成“已追平”，按真实时间流逝估算滞后。
func TestRunTrackerLagOutsideCalendarIsNotCaughtUp(t *testing.T) {
	axis, err := periodclock.AxisFor("stockcn", "5m")
	require.NoError(t, err)
	last := time.Date(2026, 12, 31, 6, 55, 0, 0, time.UTC) // 日历最后一根（北京时间 14:55）
	period, err := axis.ToAxis(last)
	require.NoError(t, err)
	now := time.Date(2027, 1, 4, 2, 0, 0, 0, time.UTC)
	summary := summaryOf("ftask", trackedRun{periodTime: period, axis: axis, status: "complete"}, now)
	require.Equal(t, last.Unix(), summary.LastPeriodTime)
	require.Equal(t, int64(now.Sub(last)/time.Second), summary.LagSeconds)
}
