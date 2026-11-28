package period

import (
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// ReplayRange 把回放区间换成日历的口径：股票市场的日线按市场当地日期回放（A 股是上海日期），两端各取所在当地日期的零点，
// [起始日期, 结束日期) 内的交易日都包含在内。页面按上海时区选日期，传来的两端是上海零点（前一日 16:00 UTC）；A 股日线的
// bar_start 是日期键（交易日期 D 的 UTC 零点，即上海 D 日 08:00），落在 [起始日期的上海零点, 结束日期的上海零点) 内当且仅当
// 起始日期 ≤ D < 结束日期，所以区间仍按上海零点记录。港股与上海同一时区，口径相同；美股日线的日期选择页面还没有适配。
// 日内的 bar_start 是真实的开盘时刻，与 crypto 一样原样返回。
func ReplayRange(calendar, bar string, start, end time.Time) (time.Time, time.Time, error) {
	if !StockDaily(calendar, bar) {
		return start, end, nil
	}
	market, _, err := sessionMarketFor(calendar)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	location := market.location
	floor := func(at time.Time) time.Time {
		local := at.In(location)
		return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
	}
	start, end = floor(start), floor(end)
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("%s日线按%s日期回放：结束日期必须晚于起始日期", market.name(), market.market.ZoneName)
	}
	return start, end, nil
}

// ReplayBars 返回 bar_start 落在 [start, end) 内的全部周期，超过 limit 根返回错误。A 股内嵌日历的最后一个交易日之后
// 没有可知的周期，枚举到它为止。
func ReplayBars(calendar, bar string, start, end time.Time, limit int) ([]PeriodBoundaries, error) {
	if !end.After(start) {
		return nil, errors.New("回放区间必须满足 start < end")
	}
	current, err := FirstBarAtOrAfter(calendar, bar, start)
	if err != nil {
		return nil, err
	}
	bars := make([]PeriodBoundaries, 0)
	for current.StorageStart.Before(end) {
		if len(bars) >= limit {
			return nil, fmt.Errorf("回放区间超过 %d 根 bar，请缩短区间", limit)
		}
		bars = append(bars, current)
		if current.NextEnd.IsZero() {
			break
		}
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return nil, err
		}
	}
	return bars, nil
}

// FirstBarAtOrAfter 返回 bar_start 不早于 at 的第一根。
func FirstBarAtOrAfter(calendar, bar string, at time.Time) (PeriodBoundaries, error) {
	if StockIntraday(calendar, bar) {
		// 交易时段的 bar 不连续：直接取不早于 at 的那一根（跨午休、隔夜与休市）。
		start, err := CeilStorageStart(calendar, bar, at)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		return FromStorageStart(calendar, bar, start)
	}
	if StockDaily(calendar, bar) {
		// 不经 ClosedPeriod 回看上一根收盘：at 落在日历首日时没有上一根。取 at 所在 UTC 日期起的第一个交易日，
		// at 晚于那天的日期键（UTC 零点）时再往后一根。
		day, err := CeilStorageStart(calendar, bar, at)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		current, err := FromStorageStart(calendar, bar, day)
		if err != nil || !current.StorageStart.Before(at) {
			return current, err
		}
		if current.NextEnd.IsZero() {
			return PeriodBoundaries{}, beyondCalendarError(calendar, at)
		}
		return FromBarEnd(calendar, bar, current.NextEnd)
	}
	current, err := ClosedPeriod(calendar, bar, at)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	for current.StorageStart.Before(at) {
		if current, err = FromBarEnd(calendar, bar, current.NextEnd); err != nil {
			return PeriodBoundaries{}, err
		}
	}
	return current, nil
}

// beyondCalendarError 说明 at 之后的周期超出了市场的内嵌日历；时间按市场当地时间写，与其余股票市场的报错一致。
func beyondCalendarError(calendar string, at time.Time) error {
	label := at.UTC().Format(time.RFC3339) + " UTC"
	market := loadedSessionMarket(calendar)
	if market != nil {
		label = market.label(at)
	}
	return &describedError{
		message: fmt.Sprintf("%s 之后的周期%s", label, translateCalendarError(market, marketcalendar.ErrNoNextTradingDay).Error()),
		cause:   marketcalendar.ErrNoNextTradingDay,
	}
}

// PeriodLabel 是给用户看的 bar 标识：股票市场日线写当地日期（A 股是上海日期：bar_start 是日期键，即上海当日 08:00，
// 日期就是交易日期；回放区间的端点是上海零点，按上海时区取日期两者都对），日内写 bar_start 的当地时间，其余写 bar_start 的 UTC 时间。
func PeriodLabel(calendar, bar string, storageStart time.Time) string {
	if market := loadedSessionMarket(calendar); market != nil {
		zone := market.market.ZoneName
		if StockDaily(calendar, bar) {
			return storageStart.In(market.location).Format("2006-01-02") + "（" + zone + "日期）"
		}
		if StockIntraday(calendar, bar) {
			return storageStart.In(market.location).Format("2006-01-02 15:04") + "（" + zone + "时间）"
		}
	}
	return storageStart.UTC().Format(time.RFC3339)
}

// BarEndLabel 是给用户看的 bar_end：UTC 时间（与回放表格的“K 线结束（UTC）”一致），股票市场日线再注明当地的交易日收盘，
// 日内再注明当地时间。
func BarEndLabel(calendar, bar string, barEnd time.Time) string {
	label := barEnd.UTC().Format(time.RFC3339)
	if market := loadedSessionMarket(calendar); market != nil {
		zone := market.market.ZoneName
		if StockDaily(calendar, bar) {
			label += "（" + zone + " " + barEnd.In(market.location).Format("2006-01-02") + " 收盘）"
		}
		if StockIntraday(calendar, bar) {
			label += "（" + zone + " " + barEnd.In(market.location).Format("2006-01-02 15:04") + "）"
		}
	}
	return label
}

// BarsBeyondStockCalendar 在 now 已过股票市场内嵌日历最后一天的收盘时，返回行键 at 晚于日历最后一天的最多根数：自然日数
// （不超过 now 当天）× 每个交易日的根数（日线 1）；其余情况为 0。now 还在日历之内时，日历之外的行只能是误写的未来数据，
// 不算超出日历。日线与日内都在当天的收盘时刻收盘。
func BarsBeyondStockCalendar(calendarID, bar string, at, now time.Time) int {
	perDay := 1
	if !StockDaily(calendarID, bar) {
		if !StockIntraday(calendarID, bar) {
			return 0
		}
		perDay = TradingDayBars(calendarID, bar)
	}
	market := loadedSessionMarket(calendarID)
	if market == nil {
		return 0
	}
	location := market.location
	last := market.calendar.LastDate()
	lastMidnight := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, location)
	if !now.After(market.closeAt(last)) {
		return 0
	}
	day, err := localDay(at, location)
	if err != nil || !day.After(last) {
		return 0
	}
	latest := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location)
	if today := now.In(location); latest.After(today) {
		latest = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
	}
	return int(latest.Sub(lastMidnight).Hours()/24+0.5) * perDay
}
