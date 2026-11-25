package pebble

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

func snapshotSeries(subjects ...string) []seriesset.Series {
	series := make([]seriesset.Series, 0, len(subjects))
	for _, subject := range subjects {
		series = append(series, seriesset.Series{SubjectID: subject, SeriesTag: goldenTag})
	}
	set, err := seriesset.Canonicalize(series)
	if err != nil {
		panic(err)
	}
	return set.Series
}

func TestRegisterSeriesSnapshotIsIdempotentAndOrdered(t *testing.T) {
	clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
	store := newGoldenStore(t, clock)
	ctx := context.Background()
	first := time.Date(2026, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
	series := snapshotSeries("BTC-USDT", "ETH-USDT")
	result, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", series, first)
	require.NoError(t, err)
	require.False(t, result.AlreadyRegistered)
	require.EqualValues(t, 2, result.ExpectedCount)

	again, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", series, first)
	require.NoError(t, err)
	require.True(t, again.AlreadyRegistered, "同 P 同 hash 的重放直接成功")

	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "SOL-USDT"), first-60)
	require.Error(t, err, "不能插入已有时间线中间")

	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "SOL-USDT"), first+30)
	require.Error(t, err, "生效周期必须对齐频率边界")

	next := first + 600
	changed, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "SOL-USDT"), next)
	require.NoError(t, err)
	require.NotEqual(t, result.SeriesHash, changed.SeriesHash)

	// P 已过去后，同 P 同 hash 的重试仍然成功。
	clock.set(time.Unix(next+3600, 0).UTC())
	replay, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "SOL-USDT"), next)
	require.NoError(t, err)
	require.True(t, replay.AlreadyRegistered)

	// 排序不规范的输入被拒绝。
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", []seriesset.Series{series[1], series[0]}, next+600)
	require.Error(t, err)
}

func TestRegisterSeriesSnapshotRejectsWhenLaterPeriodExists(t *testing.T) {
	clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	store := newGoldenStore(t, clock)
	ctx := context.Background()
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	exp := goldenRegister(t, store, clock, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), period, 15)
	// 提前建立 P+5m 那一期，再登记从 P+4m 起生效的新快照：P+4m 之后已有周期，不允许登记。
	clock.set(period.Add(2 * time.Minute))
	later := exp
	later.PeriodTime = period.Add(5 * time.Minute).Unix()
	_, err := store.CommitTimeSeriesBatch(ctx, later, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow("crypto", "bars", "1m", seriesset.Series{SubjectID: "BTC-USDT", SeriesTag: goldenTag}, period.Add(5*time.Minute), 1)}})
	require.NoError(t, err)
	var stale StaleEffectiveFromError
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Add(4*time.Minute).Unix())
	require.ErrorAs(t, err, &stale, "P 之后已有周期时不允许登记")
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Add(6*time.Minute).Unix())
	require.NoError(t, err, "已有周期之后的生效点可以登记")
}

func TestRegisterSeriesSnapshotRejectsFarFutureAndAmbiguousSeries(t *testing.T) {
	clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
	store := newGoldenStore(t, clock)
	ctx := context.Background()
	soon := time.Date(2026, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
	_, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), soon)
	require.NoError(t, err)

	// 误登记的远期 P 会让后续所有正常变更被判过期，必须在登记时拒绝。
	far := time.Date(2027, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), far)
	require.ErrorContains(t, err, "too far in the future")
	var stale StaleEffectiveFromError
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), soon-60)
	require.ErrorAs(t, err, &stale, "P 不在未来或不晚于最后登记时返回可识别的过期错误")

	// 同一 (subject_id, series_tag) 出现两次，逐行校验会有歧义。
	dup := append(snapshotSeries("BTC-USDT"), seriesset.Series{SubjectID: "btc-usdt", SeriesTag: goldenTag})
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", dup, soon+600)
	require.Error(t, err)

	// series_tag 为空的序列进不了周期完成事件的序列全集，登记时就拒绝。
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", []seriesset.Series{{SubjectID: "BTC-USDT"}}, soon+600)
	require.ErrorContains(t, err, "series_tag is required")
}

func snapshotExists(t *testing.T, store *Store, frequency, hash string) bool {
	t.Helper()
	_, err := store.loadSeriesSnapshot("crypto", "bars", frequency, hash)
	if err == nil {
		return true
	}
	var missing SeriesSnapshotMissingError
	require.ErrorAs(t, err, &missing)
	return false
}

func effectiveAt(t *testing.T, store *Store, from int64) string {
	t.Helper()
	hash, found, err := store.readEffectiveHash("crypto", "bars", "1m", from)
	require.NoError(t, err)
	require.True(t, found)
	return hash
}

// 未来、最后一条、其后没有周期的登记可以换成另一份快照；不再被引用的旧快照随之删掉。
func TestRegisterSeriesSnapshotOverridesFutureLastEntry(t *testing.T) {
	clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
	store := newGoldenStore(t, clock)
	ctx := context.Background()
	first := time.Date(2026, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
	old, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), first)
	require.NoError(t, err)
	replaced, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), first)
	require.NoError(t, err)
	require.False(t, replaced.AlreadyRegistered)
	require.EqualValues(t, 2, replaced.ExpectedCount)
	require.Equal(t, replaced.SeriesHash, effectiveAt(t, store, first))
	require.True(t, snapshotExists(t, store, "1m", replaced.SeriesHash))
	require.False(t, snapshotExists(t, store, "1m", old.SeriesHash), "被覆盖且不再被引用的快照删掉")

	// 覆盖后同 P 同 hash 的重放仍然成功。
	replay, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), first)
	require.NoError(t, err)
	require.True(t, replay.AlreadyRegistered)
}

// 被覆盖的 hash 仍被更早的条目引用时，快照保留。
func TestRegisterSeriesSnapshotOverrideKeepsReferencedSnapshot(t *testing.T) {
	clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
	store := newGoldenStore(t, clock)
	ctx := context.Background()
	first := time.Date(2026, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
	h0, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), first)
	require.NoError(t, err)
	h1, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), first+600)
	require.NoError(t, err)
	h0again, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), first+1200)
	require.NoError(t, err)
	require.Equal(t, h0.SeriesHash, h0again.SeriesHash)
	// 把最后一条（H0@first+1200）换成 H1：H0 仍被 first 那一条引用，快照保留。
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), first+1200)
	require.NoError(t, err)
	require.Equal(t, h1.SeriesHash, effectiveAt(t, store, first+1200))
	require.True(t, snapshotExists(t, store, "1m", h0.SeriesHash))
	require.True(t, snapshotExists(t, store, "1m", h1.SeriesHash))
}

// 不能覆盖的情况一律返回 effective_from_stale：P 已过、P 之后已有周期、不是最后一条。
func TestRegisterSeriesSnapshotOverrideRejections(t *testing.T) {
	ctx := context.Background()
	t.Run("P 已过", func(t *testing.T) {
		clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
		store := newGoldenStore(t, clock)
		first := time.Date(2026, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
		old, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), first)
		require.NoError(t, err)
		clock.set(time.Unix(first, 0).UTC())
		_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), first)
		var stale StaleEffectiveFromError
		require.ErrorAs(t, err, &stale)
		require.ErrorContains(t, err, "effective_from_stale")
		require.Equal(t, old.SeriesHash, effectiveAt(t, store, first))
		// P 已过后同 hash 的重放仍是成功。
		replay, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), first)
		require.NoError(t, err)
		require.True(t, replay.AlreadyRegistered)
	})
	t.Run("P 之后已有周期", func(t *testing.T) {
		clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
		store := newGoldenStore(t, clock)
		period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
		exp := goldenRegister(t, store, clock, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), period, 15)
		// 生效点 P 还在未来，但提前建立了 P 那一期：不允许再换快照。
		clock.set(period.Add(-30 * time.Second))
		_, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow("crypto", "bars", "1m", seriesset.Series{SubjectID: "BTC-USDT", SeriesTag: goldenTag}, period, 1)}})
		require.NoError(t, err)
		_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Unix())
		var stale StaleEffectiveFromError
		require.ErrorAs(t, err, &stale)
		require.Equal(t, exp.SeriesHash, effectiveAt(t, store, period.Unix()))
	})
	t.Run("不是最后一条", func(t *testing.T) {
		clock := newGoldenClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
		store := newGoldenStore(t, clock)
		first := time.Date(2026, 10, 11, 12, 5, 0, 0, time.UTC).Unix()
		old, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), first)
		require.NoError(t, err)
		_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), first+600)
		require.NoError(t, err)
		_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "SOL-USDT"), first)
		var stale StaleEffectiveFromError
		require.ErrorAs(t, err, &stale)
		require.Equal(t, old.SeriesHash, effectiveAt(t, store, first))
	})
}
