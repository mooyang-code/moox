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
		series = append(series, seriesset.Series{SubjectID: subject})
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

	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "SOL-USDT"), first)
	require.Error(t, err, "同 P 不同 hash 必须拒绝")

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
	_, err := store.CommitTimeSeriesBatch(ctx, later, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: goldenRow("crypto", "bars", "1m", seriesset.Series{SubjectID: "BTC-USDT"}, period.Add(5*time.Minute), 1)}})
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
	dup := append(snapshotSeries("BTC-USDT"), seriesset.Series{SubjectID: "btc-usdt"})
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", dup, soon+600)
	require.Error(t, err)
}
