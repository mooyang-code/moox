package pebble

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

func snapshotSeries(subjects ...string) []seriesset.Series {
	series := make([]seriesset.Series, 0, len(subjects))
	for _, subject := range subjects {
		series = append(series, seriesset.Series{Provider: "binance", SourceID: "spot_http", MarketType: "spot", SubjectID: subject})
	}
	set, err := seriesset.Canonicalize(series)
	if err != nil {
		panic(err)
	}
	return set.Series
}

func snapshotExpectation(period time.Time, subjects ...string) DatasetPeriodExpectation {
	set, err := seriesset.Canonicalize(snapshotSeries(subjects...))
	if err != nil {
		panic(err)
	}
	return DatasetPeriodExpectation{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: set.Hash, ExpectedCount: uint32(len(set.Series)), AllowedLatenessSeconds: 15}
}

func TestRegisterSeriesSnapshotIsIdempotentAndOrdered(t *testing.T) {
	clock := newPeriodReceiptClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
	store := newPeriodReceiptStore(t, clock)
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
	clock := newPeriodReceiptClock(time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC))
	store := newPeriodReceiptStore(t, clock)
	ctx := context.Background()
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	_, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT"), period.Unix())
	require.NoError(t, err)
	clock.set(period.Add(61 * time.Second))
	exp := snapshotExpectation(period, "BTC-USDT")
	_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "w1", "collector")
	require.NoError(t, err)
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Unix()-60)
	require.Error(t, err, "P 之后已有周期时不允许登记")
}

func TestCommitCreatesPeriodFromRegisteredSnapshot(t *testing.T) {
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := newPeriodReceiptClock(period.Add(61 * time.Second))
	store := newPeriodReceiptStore(t, clock)
	ctx := context.Background()
	_, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Unix())
	require.NoError(t, err)
	exp := snapshotExpectation(period, "BTC-USDT", "ETH-USDT")

	result, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "w1", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", result.Status)
	require.Equal(t, period.Add(time.Minute+15*time.Second).Unix(), result.DeadlineAt, "截止时间 = 收盘 + 允许迟到时间，由 Storage 计算")

	progress, found, err := store.PeriodStatusByKey(ctx, "crypto", "bars", "1m", period.Unix(), true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, exp.SeriesHash, progress.SeriesHash)
	require.Equal(t, []uint32{1}, PeriodMissingIndexes(progress.ExpectedCount, progress.Bitmap))

	result, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 1, Row: periodRowForTest(period, "ETH-USDT")}}, "w2", "collector")
	require.NoError(t, err)
	require.Equal(t, "complete", result.Status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestLazyCommitRejectionsAndMissingList(t *testing.T) {
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := newPeriodReceiptClock(period.Add(61 * time.Second))
	store := newPeriodReceiptStore(t, clock)
	ctx := context.Background()
	exp := snapshotExpectation(period, "BTC-USDT", "ETH-USDT")
	row := []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}

	_, err := store.CommitTimeSeriesBatch(ctx, exp, row, "w1", "collector")
	var missing SeriesSnapshotMissingError
	require.ErrorAs(t, err, &missing, "快照未登记必须拒绝")

	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Unix())
	require.NoError(t, err)
	wrong := exp
	wrong.SeriesHash = "not-the-effective-hash"
	_, err = store.CommitTimeSeriesBatch(ctx, wrong, row, "w1", "collector")
	var mismatch PeriodHashMismatchError
	require.ErrorAs(t, err, &mismatch, "与生效表不符的 hash 必须拒绝")

	// 之前的周期没有任何生效项，不能凭空建周期。
	early := snapshotExpectation(period.Add(-time.Minute), "BTC-USDT", "ETH-USDT")
	_, err = store.CommitTimeSeriesBatch(ctx, early, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period.Add(-time.Minute), "BTC-USDT")}}, "w1", "collector")
	require.ErrorAs(t, err, &missing)

	// 已过截止时间：拒绝且不建立周期。
	clock.set(period.Add(time.Minute + 15*time.Second))
	_, err = store.CommitTimeSeriesBatch(ctx, exp, row, "late", "collector")
	var late PeriodDeadlinePassedError
	require.ErrorAs(t, err, &late)
	_, found, err := store.PeriodStatusByKey(ctx, "crypto", "bars", "1m", period.Unix(), false)
	require.NoError(t, err)
	require.False(t, found, "迟到拒绝不得建立周期")

	// 另一个周期：只提交一半，到点后 degraded，缺失列表包含从未提交的标的。
	next := period.Add(time.Minute)
	clock.set(next.Add(61 * time.Second))
	exp2 := snapshotExpectation(next, "BTC-USDT", "ETH-USDT")
	_, err = store.CommitTimeSeriesBatch(ctx, exp2, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(next, "BTC-USDT")}}, "w3", "collector")
	require.NoError(t, err)
	clock.set(next.Add(time.Minute + 20*time.Second))
	status, err := store.FinalizeDatasetPeriod(ctx, exp2, clock.now())
	require.NoError(t, err)
	require.Equal(t, "degraded", status)
	marker := collectorPeriodMarkerFromOutbox(t, store)
	require.Equal(t, []string{"ETH-USDT"}, marker.GetFailedSubjects(), "从未提交的标的同样列为缺失")
}

func TestConcurrentLazyCommitsCreateOnePeriod(t *testing.T) {
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := newPeriodReceiptClock(period.Add(61 * time.Second))
	store := newPeriodReceiptStore(t, clock)
	ctx := context.Background()
	subjects := []string{"A-USDT", "B-USDT", "C-USDT", "D-USDT"}
	_, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries(subjects...), period.Unix())
	require.NoError(t, err)
	exp := snapshotExpectation(period, subjects...)
	var wg sync.WaitGroup
	errs := make([]error, len(subjects))
	for i, subject := range subjects {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: uint32(i), Row: periodRowForTest(period, subject)}}, "w-"+subject, "collector")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, countOutboxEvent(t, store, events.CollectorPeriodCompleted.Name()))
}

func TestLegacyEnsureAndCommitStillWorkAlongsideLazyPeriods(t *testing.T) {
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := newPeriodReceiptClock(period.Add(10 * time.Second))
	store := newPeriodReceiptStore(t, clock)
	ctx := context.Background()
	exp := periodExpectationForTest(period, 2)
	exp.DeadlineAt = period.Add(time.Hour).Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)
	got, err := store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "", "collector")
	require.NoError(t, err)
	require.Equal(t, "waiting", got.Status)
	// 旧周期内嵌快照，读取不依赖共享快照存储。
	progress, found, err := store.PeriodStatusByKey(ctx, "crypto", "bars", "1m", period.Unix(), true)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, progress.SeriesSnapshot, 2)
}

func TestLazyPeriodSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	period := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	clock := newPeriodReceiptClock(period.Add(61 * time.Second))
	open := func() *Store {
		store, err := Open(Options{Path: dir + "/node", NodeID: "node-a", PeriodNow: clock.now})
		require.NoError(t, err)
		return store
	}
	store := open()
	ctx := context.Background()
	_, err := store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", snapshotSeries("BTC-USDT", "ETH-USDT"), period.Unix())
	require.NoError(t, err)
	exp := snapshotExpectation(period, "BTC-USDT", "ETH-USDT")
	_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{{SeriesIndex: 0, Row: periodRowForTest(period, "BTC-USDT")}}, "w1", "collector")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store = open()
	defer store.Close()
	progress, found, err := store.PeriodStatusByKey(ctx, "crypto", "bars", "1m", period.Unix(), true)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []uint32{1}, PeriodMissingIndexes(progress.ExpectedCount, progress.Bitmap))
	// 截止 worker 能找到懒建立的周期。
	clock.set(period.Add(time.Minute + 30*time.Second))
	n, err := store.FinalizeWaitingDatasetPeriods(ctx, clock.now(), 100)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestRegisterSeriesSnapshotRejectsFarFutureAndAmbiguousSeries(t *testing.T) {
	clock := newPeriodReceiptClock(time.Date(2026, 10, 11, 12, 0, 30, 0, time.UTC))
	store := newPeriodReceiptStore(t, clock)
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
	dup := append(snapshotSeries("BTC-USDT"), seriesset.Series{Provider: "okx", SourceID: "spot_http", MarketType: "spot", SubjectID: "BTC-USDT"})
	_, err = store.RegisterSeriesSnapshot(ctx, "crypto", "bars", "1m", dup, soon+600)
	require.Error(t, err)
}
