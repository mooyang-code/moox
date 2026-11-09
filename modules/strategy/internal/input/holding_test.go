package input

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// holdingStrategy 返回只有一条 holding 规则 r 的策略。
func holdingStrategy(t *testing.T, holding string) dsl.Strategy {
	t.Helper()
	strategy, err := dsl.Parse([]byte("name: h\nrules:\n  - {id: r, type: rank, score: m, select: {top: 1}, weight: 1, holding: " + holding + "}\n"))
	require.NoError(t, err)
	return strategy
}

// barAt 返回 storageStart 那一根的周期边界。
func barAt(t *testing.T, calendar, frequency, storageStart string) PeriodBoundaries {
	t.Helper()
	at, err := time.Parse(time.RFC3339, storageStart)
	require.NoError(t, err)
	bar, err := FromStorageStart(calendar, frequency, at)
	require.NoError(t, err)
	return bar
}

// scheduledStart 返回规则 r 的批次 day 最近一个应建仓 bar 的 bar_start（上海时间或 UTC，按日历）。
func scheduledStart(t *testing.T, calendar, frequency string, strategy dsl.Strategy, bar PeriodBoundaries, day int) string {
	t.Helper()
	clocks, err := HoldingClocks(calendar, frequency, strategy, bar)
	require.NoError(t, err)
	index, ok := clocks["r"].Scheduled[day]
	if !ok {
		return ""
	}
	return startOfIndex(t, calendar, frequency, bar, index)
}

// startOfIndex 把 bar 序号换回 bar_start：从当前根按序号差回退。
func startOfIndex(t *testing.T, calendar, frequency string, bar PeriodBoundaries, index int64) string {
	t.Helper()
	end, err := AdvanceBarEnd(calendar, frequency, bar.BarEnd, int(index-bar.BarIndex))
	require.NoError(t, err)
	target, err := FromBarEnd(calendar, frequency, end)
	require.NoError(t, err)
	require.Equal(t, index, target.BarIndex)
	if calendar == "cn_stock" {
		location, _ := time.LoadLocation("Asia/Shanghai")
		return target.StorageStart.In(location).Format("2006-01-02T15:04")
	}
	return target.StorageStart.UTC().Format("2006-01-02T15:04")
}

// 加密：周、月的建仓 bar，跨月、闰年 2 月；at: first 与 last；倒数第 1 天。
func TestHoldingClocksCryptoWeekMonth(t *testing.T) {
	const cal, freq = DefaultCalendar, "1h"
	week := holdingStrategy(t, "{calendar: week, days: [1, -1], at: first}")
	// 2024-02-28 是周三：最近的周一是 02-26，第一根 00:00；最近的周日（-1）是 02-25，at first 取 00:00。
	bar := barAt(t, cal, freq, "2024-02-28T10:00:00Z")
	require.Equal(t, "2024-02-26T00:00", scheduledStart(t, cal, freq, week, bar, 1))
	require.Equal(t, "2024-02-25T00:00", scheduledStart(t, cal, freq, week, bar, -1))
	// 周一 00:00 那一根本身就是建仓 bar。
	monday := barAt(t, cal, freq, "2024-02-26T00:00:00Z")
	require.Equal(t, "2024-02-26T00:00", scheduledStart(t, cal, freq, week, monday, 1))

	month := holdingStrategy(t, "{calendar: month, days: [-1, 30]}")
	// 闰年 2 月最后一天是 29 日；at 默认 last 取 23:00 那一根。3 月 1 日 05:00 时，最近的月末是 02-29。
	march := barAt(t, cal, freq, "2024-03-01T05:00:00Z")
	require.Equal(t, "2024-02-29T23:00", scheduledStart(t, cal, freq, month, march, -1))
	// 2 月没有 30 日：最近的 30 日是 1 月 30 日（跨月）。
	require.Equal(t, "2024-01-30T23:00", scheduledStart(t, cal, freq, month, march, 30))
	// 月末当天 22:00：at last 的 23:00 还没到，取上个月的月末。
	before := barAt(t, cal, freq, "2024-02-29T22:00:00Z")
	require.Equal(t, "2024-01-31T23:00", scheduledStart(t, cal, freq, month, before, -1))
	last := barAt(t, cal, freq, "2024-02-29T23:00:00Z")
	require.Equal(t, "2024-02-29T23:00", scheduledStart(t, cal, freq, month, last, -1))
}

// 加密 trading_days：每 3 天一个周期，锚点早于与晚于当前都只决定相位。
func TestHoldingClocksCryptoTradingDaysAnchor(t *testing.T) {
	const cal, freq = DefaultCalendar, "1d"
	bar := barAt(t, cal, freq, "2024-03-10T00:00:00Z")
	early := holdingStrategy(t, `{calendar: trading_days, every: 3, days: [1], anchor: "2024-03-01"}`)
	late := holdingStrategy(t, `{calendar: trading_days, every: 3, days: [1], anchor: "2024-04-03"}`)
	// 03-01、03-04、03-07、03-10……：03-10 本身是第 1 天。04-03 往回每 3 天同样落在 03-10。
	require.Equal(t, "2024-03-10T00:00", scheduledStart(t, cal, freq, early, bar, 1))
	require.Equal(t, "2024-03-10T00:00", scheduledStart(t, cal, freq, late, bar, 1))
	second := holdingStrategy(t, `{calendar: trading_days, every: 3, days: [2], anchor: "2024-03-01"}`)
	require.Equal(t, "2024-03-08T00:00", scheduledStart(t, cal, freq, second, bar, 2))
}

// A 股日线：周、月、每 5 个交易日；节假日后的第一个交易日；当月交易日数不足。
func TestHoldingClocksStockDaily(t *testing.T) {
	const cal, freq = "cn_stock", "1d"
	// 2025 年国庆：10-01～10-08 休市，10-09（周四）是当周第 1 个交易日。
	bar := barAt(t, cal, freq, "2025-10-09T16:00:00Z") // 上海 2025-10-10 0 点
	week := holdingStrategy(t, "{calendar: week, days: [1, 3]}")
	require.Equal(t, "2025-10-09T00:00", scheduledStart(t, cal, freq, week, bar, 1))
	// 当周只有 2 个交易日，第 3 个不存在：取上一周（09-29 周一起，09-30 是第 2 个……第 3 个不存在，再往前 09-24）。
	require.Equal(t, "2025-09-24T00:00", scheduledStart(t, cal, freq, week, bar, 3))
	month := holdingStrategy(t, "{calendar: month, days: [18]}")
	// 10 月只有 17 个交易日，9 月有 22 个：最近的第 18 个交易日在 9 月。
	end := barAt(t, cal, freq, "2025-10-30T16:00:00Z")
	require.Equal(t, "2025-09-24T00:00", scheduledStart(t, cal, freq, month, end, 18))
	every := holdingStrategy(t, `{calendar: trading_days, every: 5, days: [1], anchor: "2025-09-29"}`)
	// 09-29、09-30、10-09、10-10、10-13 是第 1～5 个；10-14 开始下一个周期。
	require.Equal(t, "2025-09-29T00:00", scheduledStart(t, cal, freq, every, barAt(t, cal, freq, "2025-10-12T16:00:00Z"), 1))
	require.Equal(t, "2025-10-14T00:00", scheduledStart(t, cal, freq, every, barAt(t, cal, freq, "2025-10-13T16:00:00Z"), 1))
}

// A 股 1h 日内帧：at first 是当日第一根（9:30），at last 是收盘那一根（14:00 开始）。
func TestHoldingClocksStockIntraday(t *testing.T) {
	const cal, freq = "cn_stock", "1h"
	last := holdingStrategy(t, "{calendar: week, days: [1]}")
	first := holdingStrategy(t, "{calendar: week, days: [1], at: first}")
	// 2025-10-13 是周一；10:30 那一根时，at last 的 14:00 还没到，取上周的第 1 个交易日 10-09。
	morning := barAt(t, cal, freq, "2025-10-13T02:30:00Z")
	require.Equal(t, "2025-10-09T14:00", scheduledStart(t, cal, freq, last, morning, 1))
	require.Equal(t, "2025-10-13T09:30", scheduledStart(t, cal, freq, first, morning, 1))
	closing := barAt(t, cal, freq, "2025-10-13T06:00:00Z") // 上海 14:00
	require.Equal(t, "2025-10-13T14:00", scheduledStart(t, cal, freq, last, closing, 1))
	every := holdingStrategy(t, `{calendar: trading_days, every: 5, days: [5], anchor: "2025-09-29"}`)
	require.Equal(t, "2025-10-13T14:00", scheduledStart(t, cal, freq, every, closing, 5))
	// 月：10 月第 1 个交易日是节后的 10-09；10-13 上午时 at first 取 10-09 第一根，月末 -1 取 9 月最后一个交易日的收盘那一根。
	month := holdingStrategy(t, "{calendar: month, days: [1, -1], at: first}")
	require.Equal(t, "2025-10-09T09:30", scheduledStart(t, cal, freq, month, morning, 1))
	require.Equal(t, "2025-09-30T09:30", scheduledStart(t, cal, freq, month, morning, -1))
	monthLast := holdingStrategy(t, "{calendar: month, days: [-1]}")
	require.Equal(t, "2025-09-30T14:00", scheduledStart(t, cal, freq, monthLast, morning, -1))
}

// bars/offsets 写法的锚点：换成 bar 序号；不是 bar 起点时启用即拒绝。
func TestHoldingAnchorBar(t *testing.T) {
	strategy := holdingStrategy(t, `{bars: 4, offsets: [0], anchor: "2024-01-01T02:00:00Z"}`)
	bar := barAt(t, DefaultCalendar, "1h", "2024-01-01T05:00:00Z")
	clocks, err := HoldingClocks(DefaultCalendar, "1h", strategy, bar)
	require.NoError(t, err)
	anchor := barAt(t, DefaultCalendar, "1h", "2024-01-01T02:00:00Z")
	require.Equal(t, anchor.BarIndex, clocks["r"].Anchor)
	require.NoError(t, CheckHoldingAnchors(DefaultCalendar, "1h", strategy))

	offBar := holdingStrategy(t, `{bars: 4, offsets: [0], anchor: "2024-01-01T02:30:00Z"}`)
	err = CheckHoldingAnchors(DefaultCalendar, "1h", offBar)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "不是 1h 周期某根 bar 的起点"), err.Error())
	// A 股日线的 bar_start 是上海 0 点：UTC 0 点不是。
	stock := holdingStrategy(t, `{bars: 5, offsets: [0], anchor: "2025-10-09T00:00:00Z"}`)
	require.Error(t, CheckHoldingAnchors("cn_stock", "1d", stock))
	date := holdingStrategy(t, `{bars: 5, offsets: [0], anchor: "2025-10-09"}`)
	require.NoError(t, CheckHoldingAnchors("cn_stock", "1d", date))
	// 交易日期不是交易日。
	holiday := holdingStrategy(t, `{bars: 5, offsets: [0], anchor: "2025-10-01"}`)
	require.Error(t, CheckHoldingAnchors("cn_stock", "1d", holiday))
}
