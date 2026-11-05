package pebble

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

const (
	testSpotTag = "venue:binance|market:spot|source:spot_http"
	testSwapTag = "venue:binance|market:swap|source:swap_http"
)

// 统一数据集：同一周期的现货、永续序列，永续失败、现货成功时，failed_series 只含永续。
func TestDatasetPeriodUnifiedDatasetReportsFailedSeries(t *testing.T) {
	store := newPeriodTestStore(t)
	ctx := context.Background()
	period := time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 4, "BTC-USDT", "BTC-USDT", "ETH-USDT", "ETH-USDT")
	for i, tag := range []string{testSpotTag, testSwapTag, testSpotTag, testSwapTag} {
		exp.SeriesSnapshot[i].SeriesTag = tag
	}
	deadline := time.Now().UTC().Add(time.Minute)
	exp.DeadlineAt = deadline.Unix()
	_, err := store.EnsureDatasetPeriod(ctx, exp)
	require.NoError(t, err)

	// 序号 0、2 是现货，1、3 是永续；永续一直没有提交。
	_, err = store.CommitTimeSeriesBatch(ctx, exp, []TimeSeriesBatchItem{
		{SeriesIndex: 0, Row: periodTaggedRowForTest(period, "BTC-USDT", testSpotTag)},
		{SeriesIndex: 2, Row: periodTaggedRowForTest(period, "ETH-USDT", testSpotTag)},
	})
	require.NoError(t, err)
	_, err = store.RecordDatasetPeriodFailures(ctx, exp, []uint32{1, 3})
	require.NoError(t, err)
	status, err := store.FinalizeDatasetPeriod(ctx, exp, deadline)
	require.NoError(t, err)
	require.Equal(t, "degraded", status)
	require.Equal(t, 1, countOutboxEvent(t, store, events.DatasetPeriodCompleted.Name()))

	marker := collectorPeriodMarkerFromOutbox(t, store)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetUniverseSubjectIds())
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, marker.GetFailedSubjects(), "按标的的口径不变：任一序列失败，标的就记失败")
	require.Len(t, marker.GetUniverseSeries(), 4)
	require.Len(t, marker.GetFailedSeries(), 2)
	for _, failed := range marker.GetFailedSeries() {
		require.Equal(t, testSwapTag, failed.GetSeriesTag(), "失败序列只含永续")
	}
}

// 单序列数据集不带序列名单，事件与改动之前完全一致。
func TestDatasetPeriodSingleSeriesTagHasNoSeriesLists(t *testing.T) {
	period := time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 2)
	for i := range exp.SeriesSnapshot {
		exp.SeriesSnapshot[i].SeriesTag = testSpotTag
	}
	universe, failed := periodMarkerSeries(exp, nil)
	require.Nil(t, universe)
	require.Nil(t, failed)
}

// 序列名单让周期完成事件变大：预检按每条序列都失败估算，事件上限能容纳时通过，放不下时拒绝。
func TestUnifiedDatasetPeriodUniverseSizeIncludesSeriesLists(t *testing.T) {
	period := time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC)
	exp := periodExpectationForTest(period, 400)
	exp.SeriesSnapshot = exp.SeriesSnapshot[:0]
	for i := 0; i < 200; i++ {
		for _, tag := range []string{testSpotTag, testSwapTag} {
			exp.SeriesSnapshot = append(exp.SeriesSnapshot, DatasetPeriodSeries{SeriesIndex: uint32(len(exp.SeriesSnapshot)), SubjectID: "SYM" + string(rune('A'+i/26)) + string(rune('A'+i%26)) + "-USDT", SeriesTag: tag})
		}
	}
	store := newPeriodTestStore(t)
	store.maxEventBytes = 1 << 20
	require.NoError(t, store.validatePeriodUniverseSize(exp))
	store.maxEventBytes = 20 << 10
	require.Error(t, store.validatePeriodUniverseSize(exp), "两份序列名单都按全部序列估算，超过上限要拒绝")
}

func periodTaggedRowForTest(period time.Time, subject, tag string) *pb.RowFieldUpsert {
	row := periodRowForTest(period, subject)
	row.Key.GetTimeSeries().SeriesTag = tag
	return row
}
