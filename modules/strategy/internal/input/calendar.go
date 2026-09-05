package input

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

var ErrUnsupportedCalendar = errors.New("unsupported strategy calendar")

// ClosedBar maps a trigger timestamp to the most recent closed bar end. It
// supports the two calendars used by the personal system without pretending
// stock intraday sessions are continuous.
func ClosedBar(calendar, bar string, trigger time.Time, tradingDays []time.Time) (time.Time, error) {
	if trigger.IsZero() {
		return time.Time{}, errors.New("trigger time is required")
	}
	bar = strings.ToLower(strings.TrimSpace(bar))
	if len(bar) < 2 {
		return time.Time{}, fmt.Errorf("bar %q is invalid", bar)
	}
	n, err := strconv.Atoi(bar[:len(bar)-1])
	if err != nil || n <= 0 {
		return time.Time{}, fmt.Errorf("bar %q is invalid", bar)
	}
	suffix := bar[len(bar)-1]
	duration := time.Duration(n) * time.Minute
	if suffix == 'h' {
		duration = time.Duration(n) * time.Hour
	}
	if suffix == 'd' {
		duration = time.Duration(n) * 24 * time.Hour
	}
	if suffix != 'm' && suffix != 'h' && suffix != 'd' {
		return time.Time{}, fmt.Errorf("bar %q is invalid", bar)
	}
	calendar = normalizeCalendar(calendar)
	trigger = trigger.UTC()
	switch calendar {
	case "crypto_24x7":
		unix := trigger.UnixNano()
		end := (unix / duration.Nanoseconds()) * duration.Nanoseconds()
		return time.Unix(0, end).UTC(), nil
	case "cn_stock":
		if len(tradingDays) == 0 {
			return time.Time{}, errors.New("cn_stock trading days are required")
		}
		for i := len(tradingDays) - 1; i >= 0; i-- {
			day := tradingDays[i].UTC()
			end := time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, time.UTC)
			if !trigger.Before(end) {
				return end, nil
			}
		}
		return time.Time{}, errors.New("trigger is before calendar range")
	default:
		return time.Time{}, fmt.Errorf("%w: %s", ErrUnsupportedCalendar, calendar)
	}
}

// PeriodBoundaries is the calendar-normalized identity of one completed bar.
// Storage uses StorageStart, while Strategy/Trade use BarEnd. PreviousStart
// and NextEnd advance by valid bars rather than by wall-clock duration.
type PeriodBoundaries struct {
	StorageStart  time.Time
	PreviousStart time.Time
	BarEnd        time.Time
	NextEnd       time.Time
	BarIndex      int64
}

// ClosedPeriod returns the most recent completed bar at trigger. For the
// stock daily feed the persisted BarStart is local midnight, while the
// strategy boundary is the exchange close; the calendar bridges those two
// conventions and skips weekends/holidays.
func ClosedPeriod(calendarID, frequency string, trigger time.Time) (PeriodBoundaries, error) {
	if trigger.IsZero() {
		return PeriodBoundaries{}, errors.New("trigger time is required")
	}
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.TrimSpace(strings.ToLower(frequency))
	if calendarID == "cn_stock" && frequency == "1d" {
		calendar, err := marketcalendar.Load("cn_stock")
		if err != nil {
			return PeriodBoundaries{}, err
		}
		location, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return PeriodBoundaries{}, err
		}
		local := trigger.In(location)
		day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
		if err != nil {
			return PeriodBoundaries{}, err
		}
		closeAt := time.Date(local.Year(), local.Month(), local.Day(), 15, 0, 0, 0, location)
		if local.Before(closeAt) {
			day, err = calendar.PrevTradingDay(day)
		} else if status, statusErr := calendar.Status(day); statusErr != nil {
			return PeriodBoundaries{}, statusErr
		} else if status != marketcalendar.TradingDay {
			day, err = calendar.PrevTradingDay(day)
		}
		if err != nil {
			return PeriodBoundaries{}, err
		}
		return boundariesForStockDay(calendar, location, day)
	}
	if calendarID == "crypto_24x7" {
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		end, err := ClosedBar(calendarID, frequency, trigger, nil)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		start := end.Add(-duration)
		return PeriodBoundaries{StorageStart: start, PreviousStart: start.Add(-duration), BarEnd: end, NextEnd: end.Add(duration), BarIndex: end.UnixNano() / duration.Nanoseconds()}, nil
	}
	return PeriodBoundaries{}, fmt.Errorf("strategy calendar %q with frequency %q is unsupported", calendarID, frequency)
}

// FromStorageStart maps a Storage row key to the same public strategy
// boundary used by a scheduled trigger.
func FromStorageStart(calendarID, frequency string, storageStart time.Time) (PeriodBoundaries, error) {
	if storageStart.IsZero() {
		return PeriodBoundaries{}, errors.New("storage period is required")
	}
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.TrimSpace(strings.ToLower(frequency))
	if calendarID == "cn_stock" && frequency == "1d" {
		calendar, err := marketcalendar.Load("cn_stock")
		if err != nil {
			return PeriodBoundaries{}, err
		}
		location, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return PeriodBoundaries{}, err
		}
		local := storageStart.In(location)
		day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
		if err != nil {
			return PeriodBoundaries{}, err
		}
		status, err := calendar.Status(day)
		if err != nil || status != marketcalendar.TradingDay {
			if err == nil {
				err = fmt.Errorf("storage period %s is not a trading day", day)
			}
			return PeriodBoundaries{}, err
		}
		return boundariesForStockDay(calendar, location, day)
	}
	if calendarID == "crypto_24x7" {
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		start := storageStart.UTC()
		end := start.Add(duration)
		return PeriodBoundaries{StorageStart: start, PreviousStart: start.Add(-duration), BarEnd: end, NextEnd: end.Add(duration), BarIndex: end.UnixNano() / duration.Nanoseconds()}, nil
	}
	return PeriodBoundaries{}, fmt.Errorf("strategy calendar %q with frequency %q is unsupported", calendarID, frequency)
}

func boundariesForStockDay(calendar marketcalendar.TradingCalendar, location *time.Location, day marketcalendar.CivilDate) (PeriodBoundaries, error) {
	previous, err := calendar.PrevTradingDay(day)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	next, err := calendar.NextTradingDay(day)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC()
	previousStart := time.Date(previous.Year(), previous.Month(), previous.Day(), 0, 0, 0, 0, location).UTC()
	barEnd := time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, location).UTC()
	nextEnd := time.Date(next.Year(), next.Month(), next.Day(), 15, 0, 0, 0, location).UTC()
	tradingDays, err := calendar.TradingDays(calendar.FirstDate(), day)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	return PeriodBoundaries{StorageStart: start, PreviousStart: previousStart, BarEnd: barEnd, NextEnd: nextEnd, BarIndex: int64(len(tradingDays) - 1)}, nil
}

// AdvanceBarEnd moves by valid bars. n may be negative for calendar-aware
// lookups; zero returns end unchanged.
func AdvanceBarEnd(calendarID, frequency string, end time.Time, n int) (time.Time, error) {
	if n == 0 {
		return end.UTC(), nil
	}
	if normalizeCalendar(calendarID) == "crypto_24x7" {
		duration, durationErr := parseBarDuration(strings.ToLower(strings.TrimSpace(frequency)))
		if durationErr != nil {
			return time.Time{}, durationErr
		}
		return end.UTC().Add(time.Duration(n) * duration), nil
	}
	calendar, err := marketcalendar.Load("cn_stock")
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, err
	}
	local := end.In(location)
	day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i < absInt(n); i++ {
		if n > 0 {
			day, err = calendar.NextTradingDay(day)
		} else {
			day, err = calendar.PrevTradingDay(day)
		}
		if err != nil {
			return time.Time{}, err
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, location).UTC(), nil
}

func FromBarEnd(calendarID, frequency string, end time.Time) (PeriodBoundaries, error) {
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.ToLower(strings.TrimSpace(frequency))
	if calendarID == "cn_stock" && frequency == "1d" {
		calendar, err := marketcalendar.Load("cn_stock")
		if err != nil {
			return PeriodBoundaries{}, err
		}
		location, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return PeriodBoundaries{}, err
		}
		local := end.In(location)
		day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
		if err != nil {
			return PeriodBoundaries{}, err
		}
		return boundariesForStockDay(calendar, location, day)
	}
	if calendarID == "crypto_24x7" {
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		end = end.UTC()
		return PeriodBoundaries{StorageStart: end.Add(-duration), PreviousStart: end.Add(-2 * duration), BarEnd: end, NextEnd: end.Add(duration), BarIndex: end.UnixNano() / duration.Nanoseconds()}, nil
	}
	return PeriodBoundaries{}, fmt.Errorf("strategy calendar %q with frequency %q is unsupported", calendarID, frequency)
}

func PreviousStorageStart(calendarID, frequency string, storageStart time.Time) (time.Time, error) {
	if strings.TrimSpace(calendarID) == "" {
		duration, err := parseBarDuration(strings.ToLower(strings.TrimSpace(frequency)))
		if err != nil {
			return time.Time{}, err
		}
		return storageStart.UTC().Add(-duration), nil
	}
	p, err := FromStorageStart(calendarID, frequency, storageStart)
	if err != nil {
		return time.Time{}, err
	}
	if p.PreviousStart.IsZero() {
		return time.Time{}, errors.New("previous period is unavailable")
	}
	return p.PreviousStart, nil
}

// HistoryStart returns the start key for count completed bars ending at the
// supplied StorageStart. It intentionally uses the market calendar for
// session-based daily bars.
func HistoryStart(calendarID, frequency string, storageStart time.Time, count int) (time.Time, error) {
	if count <= 1 {
		return storageStart.UTC(), nil
	}
	if strings.TrimSpace(calendarID) == "" {
		duration, err := parseBarDuration(strings.ToLower(strings.TrimSpace(frequency)))
		if err != nil {
			return time.Time{}, err
		}
		return storageStart.UTC().Add(-time.Duration(count-1) * duration), nil
	}
	period, err := FromStorageStart(calendarID, frequency, storageStart)
	if err != nil {
		return time.Time{}, err
	}
	calendarID = normalizeCalendar(calendarID)
	if calendarID == "crypto_24x7" {
		duration, durationErr := parseBarDuration(strings.ToLower(strings.TrimSpace(frequency)))
		if durationErr != nil {
			return time.Time{}, durationErr
		}
		return storageStart.UTC().Add(-time.Duration(count-1) * duration), nil
	}
	calendar, err := marketcalendar.Load("cn_stock")
	if err != nil {
		return time.Time{}, err
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.Time{}, err
	}
	local := period.StorageStart.In(location)
	day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return time.Time{}, err
	}
	for i := 1; i < count; i++ {
		day, err = calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, err
		}
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC(), nil
}

func parseBarDuration(bar string) (time.Duration, error) {
	if len(bar) < 2 {
		return 0, fmt.Errorf("bar %q is invalid", bar)
	}
	n, err := strconv.Atoi(bar[:len(bar)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bar %q is invalid", bar)
	}
	switch bar[len(bar)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("bar %q is invalid", bar)
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func normalizeCalendar(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
