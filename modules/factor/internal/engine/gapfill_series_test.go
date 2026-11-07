package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

const (
	gapSpotTag = "venue:binance|market:spot|source:spot_http"
	gapSwapTag = "venue:binance|market:swap|source:swap_http"
)

// periodUniverse 按周期给出冻结全集（统一数据集每一期 marker 里的 universe_series），并记录读取次数。
type periodUniverse struct {
	byPeriod map[int64]storageio.Universe
	reads    int
	err      error
}

func (p *periodUniverse) ComputedUniverse(_ context.Context, _, _, _ string, periodTime int64) (storageio.Universe, bool, error) {
	p.reads++
	if p.err != nil {
		return storageio.Universe{}, false, p.err
	}
	universe, ok := p.byPeriod[periodTime]
	return universe, ok, nil
}

func seriesOf(subject string, tags ...string) []seriesset.Series {
	var out []seriesset.Series
	for _, tag := range tags {
		out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: tag})
	}
	return out
}

func unifiedFiller(f *gapFixture, universe universeReader, multi bool) {
	f.filler.universe = universe
	f.filler.unified = func(context.Context, domain.FactorTask) (bool, error) { return multi, nil }
}

// 统一数据集的时序恢复按待办周期读冻结全集：永续在第 1 期之后下架，第 1 期的冻结全集里没有它，
// 两期的期望序列不同，分成两次运行，各自带当期的全集（现货的待办不会被已下架的永续拖住）。
func TestGapFillerUnifiedTimeSeriesUsesFrozenUniversePerPeriod(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		gapT0.Unix():                  {Subjects: []string{"BTC"}, Series: seriesOf("BTC", gapSpotTag, gapSwapTag)},
		gapT0.Add(time.Minute).Unix(): {Subjects: []string{"BTC"}, Series: seriesOf("BTC", gapSpotTag)},
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 2)
	first, second := f.chunks.calls[0], f.chunks.calls[1]
	require.Equal(t, gapT0, first.start)
	require.Equal(t, gapT0.Add(time.Minute), first.end)
	require.ElementsMatch(t, seriesOf("BTC", gapSpotTag, gapSwapTag), first.selection.Series)
	require.Equal(t, gapT0.Add(time.Minute), second.start)
	require.Equal(t, gapT0.Add(2*time.Minute), second.end)
	require.Equal(t, seriesOf("BTC", gapSpotTag), second.selection.Series, "之后各期的冻结全集不含已下架的永续")
	require.Zero(t, f.ledger.PendingCount())
}

// 相邻周期的冻结全集相同就合并成一次运行；冻结全集只取这批标的涉及的序列。
func TestGapFillerUnifiedTimeSeriesMergesPeriodsWithSameUniverse(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	full := storageio.Universe{Subjects: []string{"BTC", "ETH"}, Series: append(seriesOf("BTC", gapSpotTag, gapSwapTag), seriesOf("ETH", gapSpotTag, gapSwapTag)...)}
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		gapT0.Unix(): full, gapT0.Add(time.Minute).Unix(): full, gapT0.Add(2 * time.Minute).Unix(): full,
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	for minute := 0; minute < 3; minute++ {
		f.record(t, gapT0, "BTC", minute, "evt", factor)
	}
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 1)
	call := f.chunks.calls[0]
	require.Equal(t, gapT0, call.start)
	require.Equal(t, gapT0.Add(3*time.Minute), call.end)
	require.Equal(t, seriesOf("BTC", gapSpotTag, gapSwapTag), call.selection.Series, "ETH 不在这批待办里")
	require.Equal(t, []string{"BTC"}, call.selection.Subjects)
}

// 单序列数据集（不是统一数据集）：不读 marker，保持原来的一次运行，Selection 不带序列全集。
func TestGapFillerSingleSeriesDatasetDoesNotReadFrozenUniverse(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, false)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Zero(t, universe.reads, "单序列数据集不为时序恢复读 marker")
	require.Len(t, f.chunks.calls, 1)
	require.Empty(t, f.chunks.calls[0].selection.Series)
	require.Equal(t, gapT0, f.chunks.calls[0].start)
	require.Equal(t, gapT0.Add(2*time.Minute), f.chunks.calls[0].end)
}

// 统一数据集但 marker 不存在（或没有 universe_series）：不冻结，保持原来的一次运行，期望序列由数据层兜底推出。
func TestGapFillerUnifiedWithoutFrozenSeriesFallsBackToOneRun(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		gapT0.Add(time.Minute).Unix(): {Subjects: []string{"BTC"}}, // marker 在，但没有序列全集
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, universe, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.record(t, gapT0, "BTC", 2, "", factor) // 没有触发事件：读不了 marker
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Len(t, f.chunks.calls, 1)
	require.Empty(t, f.chunks.calls[0].selection.Series)
	require.Equal(t, gapT0, f.chunks.calls[0].start)
	require.Equal(t, gapT0.Add(3*time.Minute), f.chunks.calls[0].end)
}

// 读冻结全集失败只是暂时的：保持待恢复，不运行。
func TestGapFillerUnifiedKeepsPendingWhenFrozenUniverseReadFails(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	unifiedFiller(f, &periodUniverse{err: errors.New("storage unavailable")}, true)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls)
	require.Equal(t, 1, f.ledger.PendingCount())
}

// 失效重算的生产入口（seriesFollower.run）没有单一的触发事件可读 marker：Selection 只给标的、不带 Series，
// 期望序列由补算执行器里的数据层按固定下架时长兜底推出（见 localdata.TestInvalidationProductionPathDoesNotOverwriteWithPartialUniverse），
// 不是只填充：覆盖已有的旧值。
func TestSeriesFollowerRecomputeSelectionLeavesExpectedSeriesToTheDataLayer(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	follower := &seriesFollower{sets: f.sets, source: fakeColumns{}, exec: f.chunks}
	set, _ := f.sets.SetByID("fset")
	require.NoError(t, follower.run(context.Background(), set, map[string]struct{}{"BTC": {}}, false, gapT0, gapT0.Add(time.Minute)))
	require.Len(t, f.chunks.calls, 1)
	selection := f.chunks.calls[0].selection
	require.Empty(t, selection.Series)
	require.False(t, selection.FillOnly)
	require.Equal(t, []string{"BTC"}, selection.Subjects)
}
