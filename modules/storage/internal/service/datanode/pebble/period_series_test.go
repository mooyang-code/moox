package pebble

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testSpotTag = "venue:binance|market:spot|source:spot_http"
	testSwapTag = "venue:binance|market:swap|source:swap_http"
)

func seriesExpectationForTest(period time.Time, series []DatasetPeriodSeries) DatasetPeriodExpectation {
	return DatasetPeriodExpectation{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "series-hash", ExpectedCount: uint32(len(series)), SeriesSnapshot: series}
}

// 只有一个 series_tag 的数据集同样带序列名单：全集是每个标的配上这个 tag，没有成功提交的序列记入失败名单。
func TestDatasetPeriodSingleSeriesTagHasSeriesLists(t *testing.T) {
	exp := seriesExpectationForTest(time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC), []DatasetPeriodSeries{
		{SeriesIndex: 0, SubjectID: "BTC-USDT", SeriesTag: testSpotTag}, {SeriesIndex: 1, SubjectID: "ETH-USDT", SeriesTag: testSpotTag},
	})
	success := make([]byte, bitmapSize(2))
	setBitmapBit(success, 0)
	universe, failed := periodMarkerSeries(exp, success)
	require.Len(t, universe, 2)
	require.Equal(t, testSpotTag, universe[0].GetSeriesTag())
	require.Len(t, failed, 1)
	require.Equal(t, "ETH-USDT", failed[0].GetSubjectId())
	require.Equal(t, testSpotTag, failed[0].GetSeriesTag())
}

// 序列名单让周期完成事件变大：预检按每条序列都失败估算，事件上限能容纳时通过，放不下时拒绝。
func TestUnifiedDatasetPeriodUniverseSizeIncludesSeriesLists(t *testing.T) {
	series := make([]DatasetPeriodSeries, 0, 400)
	for i := 0; i < 200; i++ {
		for _, tag := range []string{testSpotTag, testSwapTag} {
			series = append(series, DatasetPeriodSeries{SeriesIndex: uint32(len(series)), SubjectID: fmt.Sprintf("SYM%c%c-USDT", 'A'+i/26, 'A'+i%26), SeriesTag: tag})
		}
	}
	exp := seriesExpectationForTest(time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC), series)
	store := newGoldenStore(t, newGoldenClock(time.Now()))
	store.maxEventBytes = 1 << 20
	require.NoError(t, store.validatePeriodUniverseSize(exp))
	store.maxEventBytes = 20 << 10
	require.Error(t, store.validatePeriodUniverseSize(exp), "两份序列名单都按全部序列估算，超过上限要拒绝")
}

// A 股规模（5500 个标的、固定 series_tag）的周期完成事件：最坏情况（全部失败，四份名单都满）远小于事件上限 8 MiB。
func TestSingleTagDatasetPeriodEventSizeAtStockScale(t *testing.T) {
	series := make([]DatasetPeriodSeries, 0, 5500)
	for i := 0; i < 5500; i++ {
		series = append(series, DatasetPeriodSeries{SeriesIndex: uint32(i), SubjectID: fmt.Sprintf("%06d.SH", 600000+i), SeriesTag: "default"})
	}
	exp := seriesExpectationForTest(time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC), series)
	universe, _ := periodMarkerSubjects(exp, nil)
	universeSeries, _ := periodMarkerSeries(exp, nil)
	require.Len(t, universeSeries, 5500)
	raw, _, err := BuildLedgerPeriodCompletedMessage(exp.SpaceID, &LedgerPeriodCompleted{
		DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime, Status: periodDegraded, SnapshotRef: exp.SeriesHash,
		UniverseSubjectIDs: universe, FailedSubjects: universe, UniverseSeries: universeSeries, FailedSeries: universeSeries,
		CompletedAt: timestamppb.New(time.Unix(exp.PeriodTime, 0)),
	})
	require.NoError(t, err)
	t.Logf("5500 个标的、全部失败的周期完成事件 %d 字节", len(raw))
	require.Less(t, len(raw), 1<<20)
	store := newGoldenStore(t, newGoldenClock(time.Now()))
	store.maxEventBytes = 8 << 20
	require.NoError(t, store.validatePeriodUniverseSize(exp))
}
