package marketcalendar

import (
	"fmt"
	"time"
)

// Period 是交易日所属的自然周期。
type Period int

const (
	// PeriodWeek 是自然周：从周一开始到周日。
	PeriodWeek Period = iota
	// PeriodMonth 是自然月：从 1 日开始。
	PeriodMonth
)

// samePeriod 报告两个日期是否在同一个自然周（或自然月）里。
func samePeriod(a, b CivilDate, period Period) bool {
	if period == PeriodMonth {
		return a.Year() == b.Year() && a.Month() == b.Month()
	}
	return WeekStart(a) == WeekStart(b)
}

// WeekStart 返回日期所在自然周的周一。
func WeekStart(date CivilDate) CivilDate {
	at := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	back := (int(at.Weekday()) + 6) % 7 // 周一为 0
	monday := at.AddDate(0, 0, -back)
	result, err := NewCivilDate(monday.Year(), monday.Month(), monday.Day())
	if err != nil {
		return date
	}
	return result
}

// PeriodPosition 返回交易日 date 在所属自然周（或自然月）里是第几个交易日（从 1 起），以及这个周期的交易日总数。
// 周期跨过日历首尾时只数日历覆盖范围之内的交易日；非交易日返回 ErrNotTradingDay。
func (c TradingCalendar) PeriodPosition(date CivilDate, period Period) (position, total int, err error) {
	if period != PeriodWeek && period != PeriodMonth {
		return 0, 0, fmt.Errorf("不支持的周期 %d", period)
	}
	index, err := c.TradingDayIndex(date)
	if err != nil {
		return 0, 0, err
	}
	days := c.data.tradingDays
	first := index
	for first > 0 && samePeriod(days[first-1], date, period) {
		first--
	}
	last := index
	for last+1 < len(days) && samePeriod(days[last+1], date, period) {
		last++
	}
	return index - first + 1, last - first + 1, nil
}
