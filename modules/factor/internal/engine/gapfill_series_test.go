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

const gapSpotTag = "venue:binance|market:spot|source:spot_http"

// periodUniverse 按周期给出冻结全集（每一期 marker 里的 universe_series），并记录读取次数。
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

// frozenFiller 让恢复读给定的冻结全集。
func frozenFiller(f *gapFixture, universe universeReader) {
	f.filler.universe = universe
}

// 只有一个 tag 的数据集同样读 marker 里冻结的序列全集：两期全集相同，合并成一次运行，Selection 带“标的 × 唯一的 tag”。
func TestGapFillerSingleTagDatasetUsesFrozenUniverse(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	full := storageio.Universe{Subjects: []string{"BTC", "ETH"}, Series: append(seriesOf("BTC", gapSpotTag), seriesOf("ETH", gapSpotTag)...)}
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{gapT0.Unix(): full, gapT0.Add(time.Minute).Unix(): full}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	frozenFiller(f, universe)
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.record(t, gapT0, "BTC", 1, "evt-1", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())

	require.Equal(t, 2, universe.reads)
	require.Len(t, f.chunks.calls, 1)
	require.Equal(t, seriesOf("BTC", gapSpotTag), f.chunks.calls[0].selection.Series, "ETH 不在这批待办里")
	require.Equal(t, gapT0, f.chunks.calls[0].start)
	require.Equal(t, gapT0.Add(2*time.Minute), f.chunks.calls[0].end)
	require.Zero(t, f.ledger.PendingCount())
}

// marker 不存在（或没有 universe_series）：不冻结，保持原来的一次运行，期望序列由数据层兜底推出。
func TestGapFillerWithoutFrozenSeriesFallsBackToOneRun(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	universe := &periodUniverse{byPeriod: map[int64]storageio.Universe{
		gapT0.Add(time.Minute).Unix(): {Subjects: []string{"BTC"}}, // marker 在，但没有序列全集
	}}
	f := newGapFixture(t, fakeUniverse{}, factor)
	frozenFiller(f, universe)
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
func TestGapFillerKeepsPendingWhenFrozenUniverseReadFails(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	frozenFiller(f, &periodUniverse{err: errors.New("storage unavailable")})
	f.record(t, gapT0, "BTC", 0, "evt-0", factor)
	f.now = gapT0.Add(6 * time.Minute)
	f.filler.tick(context.Background())
	require.Empty(t, f.chunks.calls)
	require.Equal(t, 1, f.ledger.PendingCount())
}

// 失效重算的生产入口（seriesFollower.run）没有单一的触发事件可读 marker：Selection 只给标的、不带 Series，
// 期望序列由补算执行器里的数据层给出，不是只填充：覆盖已有的旧值。
func TestSeriesFollowerRecomputeSelectionLeavesExpectedSeriesToTheDataLayer(t *testing.T) {
	factor := gapFactor("ma", domain.FactorTypeTimeSeries, false)
	f := newGapFixture(t, fakeUniverse{}, factor)
	follower := &seriesFollower{sets: f.sets, source: fakeColumns{}, exec: f.chunks}
	set, _ := f.sets.SetByID("fset")
	err := follower.run(context.Background(), set, map[string]struct{}{"BTC": {}}, false, gapT0, gapT0.Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, f.chunks.calls, 1)
	selection := f.chunks.calls[0].selection
	require.Empty(t, selection.Series)
	require.False(t, selection.FillOnly)
	require.Equal(t, []string{"BTC"}, selection.Subjects)
}
