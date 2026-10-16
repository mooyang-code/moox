package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/marketcalendar"
	"github.com/stretchr/testify/require"
)

// staticInventory 是测试用的采集任务清单。
type staticInventory []monmetrics.TaskResultInventoryEntry

func (s staticInventory) GetAllowStale(context.Context, time.Time) (monmetrics.TaskResultInventorySnapshot, bool, error) {
	return monmetrics.TaskResultInventorySnapshot{Entries: s}, false, nil
}

var cryptoInventory = staticInventory{{SpaceID: "crypto", MarketID: "crypto", Timezone: "UTC"}}

var shanghaiZone = time.FixedZone("Asia/Shanghai", 8*60*60)

// fakeStockDays 覆盖 [from, through]：周一至周五交易，holidays 休市。
type fakeStockDays struct {
	from, through string
	holidays      map[string]bool
}

func (d fakeStockDays) Status(date marketcalendar.CivilDate) (marketcalendar.CoverageStatus, error) {
	value := date.String()
	if value < d.from || value > d.through {
		return marketcalendar.OutOfCoverage, errors.New("out of coverage")
	}
	weekday := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC).Weekday()
	if d.holidays[value] || weekday == time.Saturday || weekday == time.Sunday {
		return marketcalendar.NonTradingDay, nil
	}
	return marketcalendar.TradingDay, nil
}

func stockSpaceClock(t *testing.T, days fakeStockDays) spaceClock {
	t.Helper()
	clock, err := monmetrics.NewExchangeClock(days, shanghaiZone, []string{"09:30-11:30", "13:00-15:00"})
	require.NoError(t, err)
	return spaceClock{clock: clock}
}

func cst(day, clock string) time.Time {
	value, err := time.ParseInLocation("2006-01-02 15:04:05", day+" "+clock, shanghaiZone)
	if err != nil {
		panic(err)
	}
	return value
}

// healthyRun 是生产者刚正常运行过、最新一根 data_time 为 output、写完时刻为 arrival 的数据集；arrival 为零表示没上报到达时间。
func healthyRun(now, output, arrival time.Time) datasetValues {
	value := datasetValues{
		interval: 60, inventory: float64(now.Unix()), lastRun: float64(now.Unix()), lastSuccess: float64(now.Unix()),
		output: float64(output.Unix()),
	}
	if !arrival.IsZero() {
		value.arrival = float64(arrival.Unix())
	}
	return value
}

// 2026-10-16 是周五，10-19 是周一；国庆 10-01～10-07 休市（10-03、10-04 是周末）。
var stockDays = fakeStockDays{from: "2026-09-01", through: "2026-12-31", holidays: map[string]bool{"2026-10-01": true, "2026-10-02": true, "2026-10-05": true, "2026-10-06": true, "2026-10-07": true}}

var stockKey = datasetKey{producer: "storage", spaceID: "stockcn", datasetID: "dataset_stockcn_kline_1m", freq: "1m"}

func TestStockMinuteDelayDuringLunchBreak(t *testing.T) {
	clock := stockSpaceClock(t, stockDays)
	lastMorningBar := cst("2026-10-16", "11:29:00")

	// 准时：11:29 那根（11:30 收线）11:30:05 到，延迟 5 秒；12:10 再看仍是 5 秒，不随当前时间增长。
	onTime := datasetStatus(cst("2026-10-16", "12:10:00"), stockKey, healthyRun(cst("2026-10-16", "12:10:00"), lastMorningBar, cst("2026-10-16", "11:30:05")), testRealtimePolicy(), clock)
	require.Equal(t, "healthy", onTime.Status, onTime.Reason)
	require.True(t, onTime.DelayKnown)
	require.EqualValues(t, 5, onTime.DelaySeconds)
	require.Zero(t, onTime.WaitingBars, "午休没有应收而未到的根")
	require.True(t, onTime.LatestBarClose.Equal(cst("2026-10-16", "11:30:00")))

	// 迟到但已到：12:10 才到，延迟 40 分钟；当前状态正常（不告警），这个延迟一直保留到下一根到达。
	late := datasetStatus(cst("2026-10-16", "12:10:30"), stockKey, healthyRun(cst("2026-10-16", "12:10:30"), lastMorningBar, cst("2026-10-16", "12:10:00")), testRealtimePolicy(), clock)
	require.Equal(t, "healthy", late.Status, late.Reason)
	require.EqualValues(t, 40*60, late.DelaySeconds)
	require.Greater(t, late.DelaySeconds, late.LatenessLimitSeconds, "延迟超过允许迟到，由页面标成迟到")
	afternoon := datasetStatus(cst("2026-10-16", "12:59:00"), stockKey, healthyRun(cst("2026-10-16", "12:59:00"), lastMorningBar, cst("2026-10-16", "12:10:00")), testRealtimePolicy(), clock)
	require.EqualValues(t, 40*60, afternoon.DelaySeconds, "下一根到达前延迟保持不变")
}

func TestStockMinuteWaitingAfterTheAfternoonOpen(t *testing.T) {
	clock := stockSpaceClock(t, stockDays)
	lastMorningBar := cst("2026-10-16", "11:29:00")
	arrival := cst("2026-10-16", "11:30:05")

	// 13:00:30：下午第一根还没收线，应收的仍是上午最后一根，已到，不需要“开盘预热”跳过。
	early := datasetStatus(cst("2026-10-16", "13:00:30"), stockKey, healthyRun(cst("2026-10-16", "13:00:30"), lastMorningBar, arrival), testRealtimePolicy(), clock)
	require.Equal(t, "healthy", early.Status, early.Reason)
	require.Zero(t, early.WaitingBars)

	// 13:01:20：13:00 那根 13:01 收线还没到——等待中 20 秒，没超过允许迟到，状态仍正常。
	waiting := datasetStatus(cst("2026-10-16", "13:01:20"), stockKey, healthyRun(cst("2026-10-16", "13:01:20"), lastMorningBar, arrival), testRealtimePolicy(), clock)
	require.Equal(t, "healthy", waiting.Status, waiting.Reason)
	require.Equal(t, 1, waiting.WaitingBars)
	require.EqualValues(t, 20, waiting.OverdueSeconds)
	require.True(t, waiting.WaitingSince.Equal(cst("2026-10-16", "13:01:00")))
	require.EqualValues(t, 5, waiting.DelaySeconds, "上一根的延迟照常保留")

	// 13:25：下午的根一直没到，最早那根 13:01 收线已等 24 分钟，超过允许迟到 10 分钟，判为过期。
	stale := datasetStatus(cst("2026-10-16", "13:25:00"), stockKey, healthyRun(cst("2026-10-16", "13:25:00"), lastMorningBar, arrival), testRealtimePolicy(), clock)
	require.Equal(t, "stale", stale.Status)
	require.Equal(t, 25, stale.WaitingBars)
	require.EqualValues(t, 24*60, stale.OverdueSeconds)
	require.True(t, strings.HasPrefix(stale.Reason, "等待中："), stale.Reason)
}

func TestStockMinuteBeforeMondayOpenAndAfterHoliday(t *testing.T) {
	clock := stockSpaceClock(t, stockDays)
	friday := cst("2026-10-16", "14:59:00")
	monday := datasetStatus(cst("2026-10-19", "09:00:00"), stockKey, healthyRun(cst("2026-10-19", "09:00:00"), friday, cst("2026-10-16", "15:00:03")), testRealtimePolicy(), clock)
	require.Equal(t, "healthy", monday.Status, monday.Reason)
	require.Zero(t, monday.WaitingBars, "周一开盘前应收的是上周五最后一根")
	require.EqualValues(t, 3, monday.DelaySeconds)

	beforeHoliday := cst("2026-09-30", "14:59:00")
	holiday := datasetStatus(cst("2026-10-05", "10:00:00"), stockKey, healthyRun(cst("2026-10-05", "10:00:00"), beforeHoliday, cst("2026-09-30", "15:00:02")), testRealtimePolicy(), clock)
	require.Equal(t, "healthy", holiday.Status, holiday.Reason)
	require.Zero(t, holiday.WaitingBars, "节假日不把休市当成缺失")
}

func TestStockCalendarCoverageIsReportedNotGuessed(t *testing.T) {
	clock := stockSpaceClock(t, fakeStockDays{from: "2026-09-01", through: "2026-10-16"})
	now := cst("2026-10-19", "10:00:00")
	got := datasetStatus(now, stockKey, healthyRun(now, cst("2026-10-16", "14:59:00"), cst("2026-10-16", "15:00:03")), testRealtimePolicy(), clock)
	require.Equal(t, "unknown", got.Status)
	require.True(t, strings.HasPrefix(got.ClockIssue, "日历未覆盖"), got.ClockIssue)
	require.Equal(t, got.ClockIssue, got.Reason)
}

func TestCryptoDelayStates(t *testing.T) {
	key := datasetKey{producer: "storage", spaceID: "crypto", datasetID: "dataset_crypto_kline_1m", freq: "1m"}
	now := time.Date(2026, 10, 16, 6, 10, 30, 0, time.UTC)
	bar := time.Date(2026, 10, 16, 6, 9, 0, 0, time.UTC)

	onTime := datasetStatus(now, key, healthyRun(now, bar, bar.Add(time.Minute+5*time.Second)), testRealtimePolicy(), cryptoClock)
	require.Equal(t, "healthy", onTime.Status, onTime.Reason)
	require.EqualValues(t, 5, onTime.DelaySeconds)
	require.Zero(t, onTime.WaitingBars)

	unknownArrival := datasetStatus(now, key, healthyRun(now, bar, time.Time{}), testRealtimePolicy(), cryptoClock)
	require.False(t, unknownArrival.DelayKnown, "生产者没上报到达时间时不推算延迟")

	stalled := datasetStatus(now, key, healthyRun(now, bar.Add(-20*time.Minute), time.Time{}), testRealtimePolicy(), cryptoClock)
	require.Equal(t, "stale", stalled.Status)
	require.Equal(t, 20, stalled.WaitingBars)
	require.EqualValues(t, (19*time.Minute + 30*time.Second).Seconds(), stalled.OverdueSeconds)
}

func TestSpaceClocksPreferTheExchangeCalendar(t *testing.T) {
	builder := Builder{Inventory: staticInventory{
		{SpaceID: "stockcn", MarketID: "stockcn", Timezone: "Asia/Shanghai"},
		{SpaceID: "stockcn", MarketID: "stockcn", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Sessions: []string{"09:30-11:30", "13:00-15:00"}},
		{SpaceID: "crypto", MarketID: "crypto", Timezone: "UTC"},
	}}
	clocks := builder.spaceClocks(t.Context(), time.Now())
	require.NoError(t, clocks["stockcn"].err)
	require.NoError(t, clocks["crypto"].err)
	_, missing := clocks["mooxsys"]
	require.False(t, missing, "没有采集任务的空间不按交易日历判断")
	require.Empty(t, (Builder{}).spaceClocks(t.Context(), time.Now()))
}
