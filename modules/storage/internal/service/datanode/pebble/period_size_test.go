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

// 周期完成事件的体积预检按每个标的都失败估算（全集与失败名单各一份）：事件上限能容纳时通过，放不下时拒绝。
func TestPeriodUniverseSizeChecksWorstCase(t *testing.T) {
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
	store.maxEventBytes = 4 << 10
	require.Error(t, store.validatePeriodUniverseSize(exp), "全集与失败名单都按全部标的估算，超过上限要拒绝")
}

// A 股规模（5931 个标的）的周期完成事件：最坏情况（全部失败，全集与失败名单都满）远小于事件上限 8 MiB。
func TestPeriodEventSizeAtStockScale(t *testing.T) {
	series := make([]DatasetPeriodSeries, 0, 5931)
	for i := 0; i < 5931; i++ {
		series = append(series, DatasetPeriodSeries{SeriesIndex: uint32(i), SubjectID: fmt.Sprintf("%06d.SH", 600000+i), SeriesTag: "default"})
	}
	exp := seriesExpectationForTest(time.Date(2026, 9, 28, 4, 5, 0, 0, time.UTC), series)
	universe, _ := periodMarkerSubjects(exp, nil)
	require.Len(t, universe, 5931)
	raw, _, err := BuildLedgerPeriodCompletedMessage(exp.SpaceID, &LedgerPeriodCompleted{
		DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime, Status: periodDegraded, SnapshotRef: exp.SeriesHash,
		UniverseSubjectIDs: universe, FailedSubjects: universe,
		CompletedAt: timestamppb.New(time.Unix(exp.PeriodTime, 0)),
	})
	require.NoError(t, err)
	t.Logf("5931 个标的、全部失败的周期完成事件 %d 字节", len(raw))
	require.Less(t, len(raw), 1<<20)
	store := newGoldenStore(t, newGoldenClock(time.Now()))
	store.maxEventBytes = 8 << 20
	require.NoError(t, store.validatePeriodUniverseSize(exp))
}
