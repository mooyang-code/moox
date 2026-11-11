package klinecheck

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// A 股日线的完整性检查与补采分块：应有的时间点只有交易日的日期键（交易日期的 UTC 零点），周末与节假日不算；
// 交易日 D 的日线要过了 D+1 日 00:00 UTC 加允许迟到时间才应有；连续缺失的交易日合并成一段（不按周末拆开），
// 一年的日线一个标的只有一个分块。
func TestStockDailyCheckExpectsTradingDaysAndMergesAcrossWeekends(t *testing.T) {
	day := func(month time.Month, d int) time.Time { return time.Date(2026, month, d, 0, 0, 0, 0, time.UTC) }
	content := &marketfetch.FetchPlanContent{Datasets: map[string]*marketfetch.PlanDatasetChange{}, Functions: map[string]*marketfetch.PlanFunctionChange{}}
	change := &marketfetch.PlanDatasetChange{Changed: true, HashChanged: true, P: day(1, 5).Unix()}
	change.DatasetID, change.Frequency, change.LatenessS, change.Hash = "ds", "1d", 90, "hash:600519.XSHG"
	change.Series = append(change.Series, seriesOf("600519.XSHG"))
	content.Datasets["ds|1d"] = change
	records := []*store.FetchPlanRecord{{ID: 1, SpaceID: "stockcn", Status: store.FetchPlanApplied, Plan: mustJSON(content), Progress: "{}"}}

	now := day(11, 11).Add(5 * time.Minute) // 北京时间 11-11 08:05：11-10 的日线已到期
	rows := &fakeRows{missing: map[string]map[int64]bool{"600519.XSHG": {}}}
	checker := &Checker{Plans: fakePlans{records}, Rows: rows, Now: func() time.Time { return now }, Calendar: markets.TradingBar}

	// 09-28～10-12 跨国庆：交易日只有 09-28、09-29、09-30、10-08、10-09、10-12（10-01～10-07 休市，10-10、10-11 是周末）。
	holiday, err := checker.Check(context.Background(), Request{SpaceID: "stockcn", From: day(9, 28), To: day(10, 13), AssumeMissing: true})
	require.NoError(t, err)
	require.Equal(t, 6, holiday.Expected)
	require.Len(t, holiday.Gaps, 1, "跨节假日不拆段")
	require.Equal(t, day(9, 28), holiday.Gaps[0].From)
	require.Equal(t, day(10, 13), holiday.Gaps[0].To)
	require.Equal(t, 6, holiday.Gaps[0].Missing)

	// 读行的检查：只缺 11-06（周五）与 11-09（周一），合并成一段；起点落在周六时从周一算起。
	rows.missing["600519.XSHG"][day(11, 6).Unix()] = true
	rows.missing["600519.XSHG"][day(11, 9).Unix()] = true
	read, err := checker.Check(context.Background(), Request{SpaceID: "stockcn", From: day(10, 31), To: day(11, 12)})
	require.NoError(t, err)
	require.Equal(t, 7, read.Expected, "11-02～11-06、11-09、11-10；11-11 的日线要到 11-12 00:00 UTC 才到期")
	require.Len(t, read.Gaps, 1)
	require.Equal(t, day(11, 6), read.Gaps[0].From)
	require.Equal(t, day(11, 10), read.Gaps[0].To)
	require.Equal(t, 2, read.Gaps[0].Missing)

	// 到期之前不算应有：11-10 23:00 UTC 时 11-10 的日线还没到期。
	now = day(11, 10).Add(23 * time.Hour)
	early, err := checker.Check(context.Background(), Request{SpaceID: "stockcn", From: day(11, 9), To: day(11, 12), AssumeMissing: true})
	require.NoError(t, err)
	require.Equal(t, 1, early.Expected, "只有 11-09")

	// 补最近一年：一个标的一段缺口、一个分块。
	now = day(11, 11).Add(5 * time.Minute)
	year, err := checker.Check(context.Background(), Request{SpaceID: "stockcn", From: time.Date(2025, 11, 11, 0, 0, 0, 0, time.UTC), To: day(11, 11), AssumeMissing: true, BeforeFirstPlan: true})
	require.NoError(t, err)
	require.Len(t, year.Gaps, 1)
	require.Greater(t, year.Expected, 235)
	require.Less(t, year.Expected, 250)
	require.Len(t, splitGap(year.Gaps[0], marketdata.FrequencyDay), 1)
}
