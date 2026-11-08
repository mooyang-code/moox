package barclock

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// Hours 按交易日历和交易时段判断某个时刻是否在交易时段内，以及最近的开盘、休市、收盘时刻。
// 只对 session 市场有意义；日期超出交易日历覆盖范围时返回 ErrOutOfCoverage，不推测。
type Hours struct {
	loc   *time.Location
	days  TradingDays
	spans []span
}

// NewHours 用交易日历、时区和交易时段（形如 "09:30-11:30"）构造交易时段判断。
func NewHours(days TradingDays, location *time.Location, sessions []string) (*Hours, error) {
	if days == nil || location == nil {
		return nil, ErrNoCalendar
	}
	spans, err := parseSpans(sessions)
	if err != nil {
		return nil, err
	}
	return &Hours{loc: location, days: days, spans: spans}, nil
}

// Hours 返回 session 市场的交易时段判断；24 小时连续市场没有交易时段，返回错误。
func (m Market) Hours() (*Hours, error) {
	if m.Alignment != AlignmentSession {
		return nil, fmt.Errorf("%w：%s 是 24 小时连续市场，没有交易时段", ErrNoCalendar, m.ID)
	}
	calendar, err := marketcalendar.Load(m.CalendarID)
	if err != nil {
		return nil, fmt.Errorf("%w：%v", ErrNoCalendar, err)
	}
	location, err := time.LoadLocation(m.Timezone)
	if err != nil {
		return nil, fmt.Errorf("%w：时区 %q 无效", ErrNoCalendar, m.Timezone)
	}
	return NewHours(calendar, location, m.Sessions)
}

// Location 返回交易时段所在的时区。
func (h *Hours) Location() *time.Location { return h.loc }

func (h *Hours) dateOf(t time.Time) marketcalendar.CivilDate {
	local := t.In(h.loc)
	date, _ := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	return date
}

func (h *Hours) shift(date marketcalendar.CivilDate, days int) marketcalendar.CivilDate {
	moved := time.Date(date.Year(), date.Month(), date.Day()+days, 12, 0, 0, 0, h.loc)
	next, _ := marketcalendar.NewCivilDate(moved.Year(), moved.Month(), moved.Day())
	return next
}

func (h *Hours) at(date marketcalendar.CivilDate, offset time.Duration) time.Time {
	return time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, h.loc).Add(offset).UTC()
}

func (h *Hours) isTradingDay(date marketcalendar.CivilDate) (bool, error) {
	status, err := h.days.Status(date)
	if err != nil {
		return false, fmt.Errorf("%w：%s：%v", ErrOutOfCoverage, date, err)
	}
	return status == marketcalendar.TradingDay, nil
}

// InSession 报告 t 是否在某个交易时段内：含开盘时刻，不含收盘时刻（11:30、15:00 已休市）。
func (h *Hours) InSession(t time.Time) (bool, error) {
	date := h.dateOf(t)
	trading, err := h.isTradingDay(date)
	if err != nil || !trading {
		return false, err
	}
	for _, item := range h.spans {
		if !t.Before(h.at(date, item.open)) && t.Before(h.at(date, item.close)) {
			return true, nil
		}
	}
	return false, nil
}

// NextOpen 返回晚于 t 的最近一次开盘时刻（09:30 或 13:00），跨午休、周末和长假。
func (h *Hours) NextOpen(t time.Time) (time.Time, error) {
	date := h.dateOf(t)
	for i := 0; i <= maxWalkDays; i, date = i+1, h.shift(date, 1) {
		trading, err := h.isTradingDay(date)
		if err != nil {
			return time.Time{}, err
		}
		if !trading {
			continue
		}
		for _, item := range h.spans {
			if open := h.at(date, item.open); open.After(t) {
				return open, nil
			}
		}
	}
	return time.Time{}, ErrNoBucket
}

// LastBreak 返回不晚于 t 的最近一次休市开始时刻，即某个交易时段的收盘（11:30 或 15:00）。
func (h *Hours) LastBreak(t time.Time) (time.Time, error) {
	return h.lastClose(t, false)
}

// LastClose 返回不晚于 t 的最近一次日收盘，即交易日最后一个交易时段的收盘（15:00）。
func (h *Hours) LastClose(t time.Time) (time.Time, error) {
	return h.lastClose(t, true)
}

func (h *Hours) lastClose(t time.Time, dailyOnly bool) (time.Time, error) {
	date := h.dateOf(t)
	for i := 0; i <= maxWalkDays; i, date = i+1, h.shift(date, -1) {
		trading, err := h.isTradingDay(date)
		if err != nil {
			return time.Time{}, err
		}
		if !trading {
			continue
		}
		for j := len(h.spans) - 1; j >= 0; j-- {
			if dailyOnly && j != len(h.spans)-1 {
				break
			}
			if closeAt := h.at(date, h.spans[j].close); !closeAt.After(t) {
				return closeAt, nil
			}
		}
	}
	return time.Time{}, ErrNoBucket
}
