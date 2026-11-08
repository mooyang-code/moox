package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

func minuteAt(n int) time.Time { return gapT0.Add(time.Duration(n) * time.Minute) }

func frozen(subjects ...string) storageio.Universe {
	var series []seriesset.Series
	for _, subject := range subjects {
		series = append(series, seriesOf(subject, gapSpotTag, gapSwapTag)...)
	}
	return storageio.Universe{Subjects: subjects, Series: series}
}

// 同一轮里有的周期有冻结全集、有的没有（marker 没有序列全集、没有触发事件）：有冻结全集的周期与没有的周期各自成段，
// 没有冻结全集的相邻周期合并成一次运行（期望序列由数据层兜底推出）。
func TestGapFillerMixesPeriodsWithAndWithoutFrozenUniverse(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		minuteAt(0).Unix(): frozen("BTC"),
		minuteAt(1).Unix(): {Subjects: []string{"BTC"}}, // marker 在，但没有序列全集
		minuteAt(3).Unix(): frozen("BTC"),
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.record(t, gapT0, "BTC", 2, "", factor) // 没有触发事件
	f.record(t, gapT0, "BTC", 3, "evt-3", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 3)
	require.Equal(t, seriesOf("BTC", gapSpotTag, gapSwapTag), f.chunks.calls[0].selection.Series)
	require.Equal(t, minuteAt(0), f.chunks.calls[0].start)
	require.Equal(t, minuteAt(1), f.chunks.calls[0].end)
	require.Empty(t, f.chunks.calls[1].selection.Series, "没有冻结全集的相邻周期合并")
	require.Equal(t, minuteAt(1), f.chunks.calls[1].start)
	require.Equal(t, minuteAt(3), f.chunks.calls[1].end)
	require.Equal(t, seriesOf("BTC", gapSpotTag, gapSwapTag), f.chunks.calls[2].selection.Series)
	require.Equal(t, minuteAt(3), f.chunks.calls[2].start)
	require.Zero(t, f.ledger.PendingCount())
}

// 不相邻的周期即使冻结全集相同也不合并：中间那些没有待办的周期不该被重算。
func TestGapFillerDoesNotMergeNonAdjacentPeriods(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	full := frozen("BTC")
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{minuteAt(0).Unix(): full, minuteAt(3).Unix(): full}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 3, "evt-3", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 2)
	require.Equal(t, minuteAt(1), f.chunks.calls[0].end)
	require.Equal(t, minuteAt(3), f.chunks.calls[1].start)
	require.Equal(t, minuteAt(4), f.chunks.calls[1].end)
}

// 周期长于 6 小时（1d 任务）：相邻的日线周期按“一个周期”判断相邻、冻结全集相同才合并，相隔一天的不合并。
func TestGapFillerDailyTaskMergesOnlyAdjacentDays(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	day := 24 * time.Hour
	full := frozen("BTC")
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		gapT0.Unix(): full, gapT0.Add(day).Unix(): full, gapT0.Add(3 * day).Unix(): full,
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	set := f.sets.sets["fset"]
	set.Set.Freq = "1d"
	f.filler.cfg.GiveUpAfter = 10 * day // 日线任务的待办要等好几天：不能被 6 小时的放弃时限先记 gave_up
	f.sets.sets["fset"] = set
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 24*60, "evt-1", factor)
	f.record(t, gapT0, "BTC", 3*24*60, "evt-3", factor)
	f.now = gapT0.Add(4 * day)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 2)
	require.Equal(t, gapT0, f.chunks.calls[0].start)
	require.Equal(t, gapT0.Add(2*day), f.chunks.calls[0].end, "相邻的两天合并")
	require.Equal(t, gapT0.Add(3*day), f.chunks.calls[1].start)
	require.Equal(t, gapT0.Add(4*day), f.chunks.calls[1].end)
}

// 本批之外的标的上下架不切碎批次：冻结全集先按本批标的过滤再比较，SOL 的永续只在第 0 期的全集里，BTC 两期的序列相同，仍是一次运行。
func TestGapFillerOtherSubjectsListingDoesNotSplitTheBatch(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		minuteAt(0).Unix(): frozen("BTC", "SOL"),
		minuteAt(1).Unix(): frozen("BTC"),
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 1)
	require.Equal(t, minuteAt(2), f.chunks.calls[0].end)
	require.Equal(t, seriesOf("BTC", gapSpotTag, gapSwapTag), f.chunks.calls[0].selection.Series)
}

// 同一轮里同一（数据集、触发事件、周期）的 marker 只读一次：两个批的区间不同、都含第 0 期时第 0 期不重复读。
func TestGapFillerReadsEachMarkerOncePerSweep(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	full := frozen("BTC", "ETH")
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{minuteAt(0).Unix(): full, minuteAt(1).Unix(): full}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.record(t, gapT0, "ETH", 0, "evt-0", factor) // 区间不同：与 BTC 分两批
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 2)
	require.Equal(t, 2, universe.reads, "第 0 期、第 1 期各读一次，不随批数重复")
}

// 只有截面成员的待办的周期不会多出时序运行：截面成员由 fillCross 按周期恢复。
func TestGapFillerTimeSeriesRunsIgnoreCrossSectionPeriods(t *testing.T) {
	ts := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	cross := gapFactor("rank", domain.FactorTypeCrossSection, true)
	full := frozen("BTC")
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{minuteAt(0).Unix(): full, minuteAt(1).Unix(): full, minuteAt(2).Unix(): full}}
	f := newGapFixture(t, fakeUniverse{subjects: []string{"BTC"}, found: true}, ts, cross)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", ts)
	f.record(t, gapT0, "BTC", 1, "evt-1", cross) // 第 1 期只有截面成员的待办
	f.record(t, gapT0, "BTC", 2, "evt-2", ts)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	var timeSeriesRuns []runCall
	for _, call := range f.chunks.calls {
		for _, member := range call.selection.Members {
			if member.Key == "ma" {
				timeSeriesRuns = append(timeSeriesRuns, call)
			}
		}
	}
	require.Len(t, timeSeriesRuns, 2, "第 0、2 期不相邻，第 1 期的截面待办不在时序运行里")
	require.Equal(t, minuteAt(1), timeSeriesRuns[0].end)
	require.Equal(t, minuteAt(2), timeSeriesRuns[1].start)
}

// flakyUniverse 对指定周期读 marker 报错，其余照常。
type flakyUniverse struct {
	periodUniverse
	failAt int64
}

func (f *flakyUniverse) ComputedUniverse(ctx context.Context, space, dataset, trigger string, periodTime int64) (storageio.Universe, bool, error) {
	if periodTime == f.failAt {
		return storageio.Universe{}, false, errors.New("storage unavailable")
	}
	return f.periodUniverse.ComputedUniverse(ctx, space, dataset, trigger, periodTime)
}

// 读 marker 失败只让这一期留待下一轮，其余各期照常运行并确认。
func TestGapFillerMarkerReadFailureOnlyHoldsThatPeriod(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	full := frozen("BTC")
	universe := &flakyUniverse{periodUniverse: periodUniverse{byPeriod: map[int64]storageio.Universe{
		minuteAt(0).Unix(): full, minuteAt(1).Unix(): full, minuteAt(2).Unix(): full,
	}}, failAt: minuteAt(1).Unix()}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	for minute := 0; minute < 3; minute++ {
		f.record(t, gapT0, "BTC", minute, "evt", factor)
	}
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 2, "第 0、2 期运行，第 1 期不运行")
	require.Equal(t, minuteAt(0), f.chunks.calls[0].start)
	require.Equal(t, minuteAt(1), f.chunks.calls[0].end)
	require.Equal(t, minuteAt(2), f.chunks.calls[1].start)
	pending := f.ledger.Pending()
	require.Len(t, pending, 1)
	require.Equal(t, minuteAt(1), pending[0].Period, "读失败的那一期留待下一轮")
}

// marker 晚于记待办：预热中跳过的项在任务就绪后不等 Settle，这时 marker 可能还没写。读不到 marker 且待办刚记下不足 Settle 时保留待办，
// 不退回兜底推导；超过 Settle 仍读不到才按原来的兜底运行。
func TestGapFillerHoldsPendingWhileMarkerMayStillBeWritten(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.filler.warmup = func() []pipeline.WarmupStatus {
		return []pipeline.WarmupStatus{{TaskID: "fset", State: pipeline.WarmupReady}}
	}
	f.recordWarming(t, "BTC", 0, "evt-0", factor) // 发现于 gapT0

	f.now = gapT0.Add(time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls, "刚记下，marker 可能还没写：保留")
	require.Equal(t, 1, f.ledger.PendingCount())

	universe.byPeriod[minuteAt(0).Unix()] = frozen("BTC") // marker 写好了
	f.now = gapT0.Add(2 * time.Minute)
	f.filler.tick(context.Background())
	require.Len(t, f.chunks.calls, 1)
	require.Equal(t, seriesOf("BTC", gapSpotTag, gapSwapTag), f.chunks.calls[0].selection.Series)
	require.Zero(t, f.ledger.PendingCount())

	// 超过 Settle 仍读不到 marker：按原逻辑退回兜底，一次运行、不带序列。
	late := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(late, &periodUniverse{byPeriod: map[int64]storageio.Universe{}}, true)
	late.record(t, gapT0, "BTC", 0, "evt-0", factor)
	late.now = gapT0.Add(6 * time.Minute)
	late.filler.tick(context.Background())
	require.Len(t, late.chunks.calls, 1)
	require.Empty(t, late.chunks.calls[0].selection.Series)
}

// 截面恢复同样：读不到 marker 且待办刚记下不足 Settle 先保留，不直接 gave_up；超过 Settle 才按原逻辑放弃。
func TestGapFillerCrossSectionHoldsWhileMarkerMayStillBeWritten(t *testing.T) {
	factor := gapFactor("rank", domain.FactorTypeCrossSection, true)
	f := newGapFixture(t, fakeUniverse{found: false}, factor)
	f.filler.warmup = func() []pipeline.WarmupStatus {
		return []pipeline.WarmupStatus{{TaskID: "fset", State: pipeline.WarmupReady}}
	}
	f.recordWarming(t, "BTC", 0, "evt-0", factor)

	f.now = gapT0.Add(time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls)
	require.Equal(t, 1, f.ledger.PendingCount(), "marker 可能还没写：保留，不 gave_up")

	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls)
	require.Zero(t, f.ledger.PendingCount(), "超过 Settle 仍读不到：按原逻辑 gave_up")
}

// “未找到”也缓存：同一轮里同一（数据集、触发事件、周期）读不到 marker 只读一次；读失败不缓存，下一次还会再读；新一轮清空缓存。
func TestGapFillerCachesMarkerNotFoundWithinSweep(t *testing.T) {
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{}}
	filler := &gapFiller{universe: universe}
	set := domain.FactorTask{SpaceID: "crypto", ResultDatasetID: "dataset_result"}
	for i := 0; i < 3; i++ {
		_, found, err := filler.computedUniverse(context.Background(), set, "evt-0", minuteAt(0).Unix())
		require.NoError(t, err)
		require.False(t, found)
	}
	require.Equal(t, 1, universe.reads, "未找到同一轮只读一次")
	_, _, err := filler.computedUniverse(context.Background(), set, "evt-1", minuteAt(0).Unix())
	require.NoError(t, err)
	require.Equal(t, 2, universe.reads, "触发事件不同是另一个键")

	failing := &periodUniverse{byPeriod: map[int64]storageio.Universe{}, err: errors.New("boom")}
	filler = &gapFiller{universe: failing}
	for i := 0; i < 2; i++ {
		_, _, err := filler.computedUniverse(context.Background(), set, "evt-0", minuteAt(0).Unix())
		require.Error(t, err)
	}
	require.Equal(t, 2, failing.reads, "读失败不缓存")
}
