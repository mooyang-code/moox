package pebble

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	testSpotTag = "venue:binance|market:spot|source:spot_http"
	testSwapTag = "venue:binance|market:swap|source:swap_http"
)

func seriesExpectationForTest(period time.Time, series []DatasetPeriodSeries) DatasetPeriodExpectation {
	return DatasetPeriodExpectation{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "series-hash", ExpectedCount: uint32(len(series)), SeriesSnapshot: series}
}

// 单序列数据集不带序列名单，事件与改动之前完全一致。
func TestDatasetPeriodSingleSeriesTagHasNoSeriesLists(t *testing.T) {
	exp := seriesExpectationForTest(time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC), []DatasetPeriodSeries{
		{SeriesIndex: 0, SubjectID: "BTC-USDT", SeriesTag: testSpotTag}, {SeriesIndex: 1, SubjectID: "ETH-USDT", SeriesTag: testSpotTag},
	})
	universe, failed := periodMarkerSeries(exp, nil)
	require.Nil(t, universe)
	require.Nil(t, failed)
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
