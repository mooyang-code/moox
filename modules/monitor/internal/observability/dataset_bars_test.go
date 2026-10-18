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
	// 没有交易日历的空间（mooxsys 系统数据）不判断到达延迟，也不给提示。
	now := time.Date(2026, 10, 16, 8, 0, 30, 0, time.UTC)
	system := datasetStatus(now, datasetKey{producer: "storage_view", spaceID: "mooxsys", datasetID: "view_mooxsys_host_resource", freq: "1m"},
		healthyRun(now, now.Add(-time.Minute), now), testRealtimePolicy(), spaceClock{err: monmetrics.ErrNoTradingCalendar})
	require.Equal(t, "healthy", system.Status)
	require.Empty(t, system.ClockIssue)
	require.False(t, system.DelayKnown)
	require.Zero(t, system.WaitingBars)
	require.Empty(t, (Builder{}).spaceClocks(t.Context(), time.Now()))
}

// 生产者每 30 秒上报一次（整分与半分对齐）：预期 K 线收线后，要等生产者下一次上报才知道它到没到。
// 已晚多久按当前时间算；上报那一刻它已晚超过允许迟到仍没有，才判为过期。
func TestExpectedBarIsJudgedByTheProducerReport(t *testing.T) {
	key := datasetKey{producer: "storage", spaceID: "crypto", datasetID: "dataset_crypto_kline_1m", freq: "1m"}
	policy := testRealtimePolicy()
	policy.Defaults.MinuteLateness = 30 * time.Second
	at := func(clock string) time.Time {
		value, err := time.Parse("2006-01-02 15:04:05.000", "2026-10-16 "+clock)
		require.NoError(t, err)
		return value
	}
	latest := at("18:54:00.000") // 18:55 收线，18:55:01 写完
	run := func(now, reportedAt time.Time) DatasetFrequencyStatus {
		value := healthyRun(now, latest, at("18:55:01.000"))
		value.reportedAt, value.confirmedAt = reportedAt, reportedAt
		return datasetStatus(now, key, value, policy, cryptoClock)
	}

	// 18:55:40：应收的就是 18:54 那根，已到，预期 K 线与最新 K 线相同。
	arrived := run(at("18:55:40.000"), at("18:55:30.002"))
	require.Equal(t, "healthy", arrived.Status, arrived.Reason)
	require.True(t, arrived.ExpectedBarAt.Equal(latest))
	require.True(t, arrived.ExpectedBarClose.Equal(at("18:55:00.000")))
	require.Zero(t, arrived.WaitingBars)

	// 18:56:25：预期 K 线变成 18:55 那根（18:56 收线），18:56:00.002 的上报里还没有它，已晚 25 秒，在允许迟到之内。
	waiting := run(at("18:56:25.000"), at("18:56:00.002"))
	require.Equal(t, "healthy", waiting.Status, waiting.Reason)
	require.False(t, waiting.LateConfirmed)
	require.True(t, waiting.ExpectedBarAt.Equal(at("18:55:00.000")))
	require.Equal(t, 1, waiting.WaitingBars)
	require.True(t, waiting.WaitingSince.Equal(at("18:56:00.000")))
	require.EqualValues(t, 25, waiting.OverdueSeconds)
	require.EqualValues(t, 1, waiting.DelaySeconds, "上一根的延迟照常保留")

	// 18:56:30.500：已晚 30.5 秒超过允许迟到，但 18:56:30 那次上报还没处理到（最近一次仍是 18:56:00），不能判过期——
	// 它可能早已写完，只是还没上报。
	beforeReport := run(at("18:56:30.500"), at("18:56:00.002"))
	require.Equal(t, "healthy", beforeReport.Status, beforeReport.Reason)
	require.EqualValues(t, 30, beforeReport.OverdueSeconds)
	require.False(t, beforeReport.LateConfirmed)

	// 超过 1 分钟没有新上报（漏报或停止上报）：不能再按上报时刻判断，按当前时间算，已晚 45 秒判过期。
	missed := run(at("18:56:45.000"), at("18:55:30.002"))
	require.Equal(t, "stale", missed.Status)
	require.True(t, missed.LateConfirmed)

	// 18:56:30.002 的上报里仍没有：确认时已晚 30.002 秒，超过允许迟到 30 秒，判为过期并告警。
	stale := run(at("18:56:31.000"), at("18:56:30.002"))
	require.Equal(t, "stale", stale.Status)
	require.True(t, stale.LateConfirmed)
	require.True(t, strings.HasPrefix(stale.Reason, "等待中：10-17 02:56 收线的那根已晚 31 秒 仍未到（缺 1 根，允许迟到 30 秒）"), stale.Reason)

	// 继续没到：18:56 那根也已收线，预期 K 线跟着前进，缺 2 根，已晚从最早缺的那根算。
	later := run(at("18:57:05.000"), at("18:57:00.002"))
	require.Equal(t, "stale", later.Status)
	require.Equal(t, 2, later.WaitingBars)
	require.True(t, later.ExpectedBarAt.Equal(at("18:56:00.000")))
	require.EqualValues(t, 65, later.OverdueSeconds)

	// 生产者时钟比 Monitor 快：上报时刻晚于当前时间时按当前时间算。
	require.Equal(t, "stale", run(at("18:57:05.000"), at("18:57:30.000")).Status)
}

// 没有产出时也给出预期 K 线；无法按交易日历判断时不给。
func TestExpectedBarWithoutOutputAndWithoutCalendar(t *testing.T) {
	key := datasetKey{producer: "storage", spaceID: "crypto", datasetID: "dataset_crypto_kline_1h", freq: "1h"}
	now := time.Date(2026, 10, 16, 18, 56, 0, 0, time.UTC)
	empty := datasetStatus(now, key, healthyRun(now, time.Time{}, time.Time{}), testRealtimePolicy(), cryptoClock)
	require.True(t, empty.ExpectedBarAt.Equal(time.Date(2026, 10, 16, 17, 0, 0, 0, time.UTC)), "18:56 应收的 1h 是 17:00 那根")
	require.True(t, empty.ExpectedBarClose.Equal(time.Date(2026, 10, 16, 18, 0, 0, 0, time.UTC)))
	require.Zero(t, empty.WaitingBars)

	system := datasetStatus(now, key, healthyRun(now, now.Add(-2*time.Hour), now), testRealtimePolicy(), spaceClock{err: monmetrics.ErrNoTradingCalendar})
	require.True(t, system.ExpectedBarAt.IsZero())
	require.True(t, system.ExpectedBarClose.IsZero())
}

func TestConfirmedUntilUsesTheEarliestReportingInstance(t *testing.T) {
	base := time.Date(2026, 10, 16, 18, 56, 0, 0, time.UTC)
	fresh := func(offset time.Duration) datasetValues {
		return datasetValues{reportedAt: base.Add(offset), confirmedAt: base.Add(offset)}
	}
	stopped := datasetValues{reportedAt: base.Add(-10 * time.Minute), confirmedAt: base.Add(-10 * time.Minute), reporterStale: true}

	// 两个实例都在上报：最新一根取最大值，要到较早上报的那个实例也上报过才能确认未到。
	require.True(t, confirmedUntil(fresh(20*time.Second), fresh(5*time.Second)).Equal(base.Add(5*time.Second)))
	require.True(t, confirmedUntil(fresh(5*time.Second), fresh(20*time.Second)).Equal(base.Add(5*time.Second)))
	// 已停止上报的实例不拖住确认，先并入哪个都一样。
	require.True(t, confirmedUntil(fresh(0), stopped).Equal(base))
	require.True(t, confirmedUntil(stopped, fresh(0)).Equal(base))
	// 没有上报时间的实例不参与。
	require.True(t, confirmedUntil(fresh(0), datasetValues{}).Equal(base))
	require.True(t, confirmedUntil(datasetValues{}, fresh(0)).Equal(base))
}
