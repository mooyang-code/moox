package input

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// ErrUnsupportedCalendar 表示策略日历与频率的组合不受支持。
var ErrUnsupportedCalendar = errors.New("不支持的策略日历")

// PeriodBoundaries 是一根已闭合 bar 在日历上的标识：Storage 用 StorageStart（bar_start）作行键，
// Strategy 与 Trade 用 BarEnd。PreviousStart 与 NextEnd 按有效 bar 推进，而不是按墙钟时长。
type PeriodBoundaries struct {
	StorageStart  time.Time
	PreviousStart time.Time
	BarEnd        time.Time
	NextEnd       time.Time
	BarIndex      int64
}

// ClosedPeriod 返回 trigger 时刻最近一根已闭合的 bar。A 股日线的 bar_start 是上海时间零点，
// 策略边界是 15:00 收盘，日历负责在两者之间换算并跳过周末与节假日。
func ClosedPeriod(calendarID, frequency string, trigger time.Time) (PeriodBoundaries, error) {
	if trigger.IsZero() {
		return PeriodBoundaries{}, errors.New("缺少触发时间")
	}
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.TrimSpace(strings.ToLower(frequency))
	if calendarID == "cn_stock" && frequency == "1d" {
		calendar, location, err := stockCalendar()
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
	if calendarID == DefaultCalendar {
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		// 加密货币 24x7：bar 按 Unix 纪元对齐，最近闭合的 bar_end 是 trigger 向下取整到 bar 时长的整数倍。
		end := time.Unix(0, (trigger.UTC().UnixNano()/duration.Nanoseconds())*duration.Nanoseconds()).UTC()
		start := end.Add(-duration)
		return PeriodBoundaries{StorageStart: start, PreviousStart: start.Add(-duration), BarEnd: end, NextEnd: end.Add(duration), BarIndex: end.UnixNano() / duration.Nanoseconds()}, nil
	}
	return PeriodBoundaries{}, fmt.Errorf("%w：日历 %q 不支持频率 %q", ErrUnsupportedCalendar, calendarID, frequency)
}

// FromStorageStart 把 Storage 的行键（bar_start）映射为策略使用的周期边界。
func FromStorageStart(calendarID, frequency string, storageStart time.Time) (PeriodBoundaries, error) {
	if storageStart.IsZero() {
		return PeriodBoundaries{}, errors.New("缺少 Storage 周期时间")
	}
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.TrimSpace(strings.ToLower(frequency))
	if calendarID == "cn_stock" && frequency == "1d" {
		calendar, location, err := stockCalendar()
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
				err = fmt.Errorf("Storage 周期 %s 不是交易日", day)
			}
			return PeriodBoundaries{}, err
		}
		return boundariesForStockDay(calendar, location, day)
	}
	if calendarID == DefaultCalendar {
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		start := storageStart.UTC()
		end := start.Add(duration)
		return PeriodBoundaries{StorageStart: start, PreviousStart: start.Add(-duration), BarEnd: end, NextEnd: end.Add(duration), BarIndex: end.UnixNano() / duration.Nanoseconds()}, nil
	}
	return PeriodBoundaries{}, fmt.Errorf("%w：日历 %q 不支持频率 %q", ErrUnsupportedCalendar, calendarID, frequency)
}

// FromBarEnd 把 bar_end 映射为周期边界。
func FromBarEnd(calendarID, frequency string, end time.Time) (PeriodBoundaries, error) {
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.ToLower(strings.TrimSpace(frequency))
	if calendarID == "cn_stock" && frequency == "1d" {
		calendar, location, err := stockCalendar()
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
	if calendarID == DefaultCalendar {
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		end = end.UTC()
		return PeriodBoundaries{StorageStart: end.Add(-duration), PreviousStart: end.Add(-2 * duration), BarEnd: end, NextEnd: end.Add(duration), BarIndex: end.UnixNano() / duration.Nanoseconds()}, nil
	}
	return PeriodBoundaries{}, fmt.Errorf("%w：日历 %q 不支持频率 %q", ErrUnsupportedCalendar, calendarID, frequency)
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
	index, err := calendar.TradingDayIndex(day)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	return PeriodBoundaries{StorageStart: start, PreviousStart: previousStart, BarEnd: barEnd, NextEnd: nextEnd, BarIndex: int64(index)}, nil
}

// AdvanceBarEnd 按有效 bar 移动 bar_end：n 为负表示向前回溯，0 原样返回。
func AdvanceBarEnd(calendarID, frequency string, end time.Time, n int) (time.Time, error) {
	if n == 0 {
		return end.UTC(), nil
	}
	if normalizeCalendar(calendarID) == DefaultCalendar {
		duration, durationErr := parseBarDuration(strings.ToLower(strings.TrimSpace(frequency)))
		if durationErr != nil {
			return time.Time{}, durationErr
		}
		shift, err := barsDuration(absInt(n), duration)
		if err != nil {
			return time.Time{}, err
		}
		if n < 0 {
			shift = -shift
		}
		return end.UTC().Add(shift), nil
	}
	calendar, location, err := stockCalendar()
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

// PreviousStorageStart 返回上一根 bar 的 bar_start。
func PreviousStorageStart(calendarID, frequency string, storageStart time.Time) (time.Time, error) {
	period, err := FromStorageStart(calendarID, frequency, storageStart)
	if err != nil {
		return time.Time{}, err
	}
	if period.PreviousStart.IsZero() {
		return time.Time{}, errors.New("无法确定上一周期")
	}
	return period.PreviousStart, nil
}

// HistoryStart 返回以 storageStart 为最后一根、共 count 根 bar 的第一根 bar_start；A 股日线按交易日回退。
func HistoryStart(calendarID, frequency string, storageStart time.Time, count int) (time.Time, error) {
	if count <= 1 {
		return storageStart.UTC(), nil
	}
	period, err := FromStorageStart(calendarID, frequency, storageStart)
	if err != nil {
		return time.Time{}, err
	}
	if normalizeCalendar(calendarID) == DefaultCalendar {
		duration, durationErr := parseBarDuration(strings.ToLower(strings.TrimSpace(frequency)))
		if durationErr != nil {
			return time.Time{}, durationErr
		}
		shift, err := barsDuration(count-1, duration)
		if err != nil {
			return time.Time{}, err
		}
		return storageStart.UTC().Add(-shift), nil
	}
	calendar, location, err := stockCalendar()
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

var (
	stockCalendarOnce     sync.Once
	stockCalendarValue    marketcalendar.TradingCalendar
	stockCalendarLocation *time.Location
	stockCalendarErr      error
)

// stockCalendar 返回 A 股交易日历与上海时区。内嵌日历的解析与校验较重，进程内只做一次；日历本身不可变。
func stockCalendar() (marketcalendar.TradingCalendar, *time.Location, error) {
	stockCalendarOnce.Do(func() {
		stockCalendarValue, stockCalendarErr = marketcalendar.Load("cn_stock")
		if stockCalendarErr != nil {
			return
		}
		stockCalendarLocation, stockCalendarErr = time.LoadLocation("Asia/Shanghai")
	})
	if stockCalendarErr != nil {
		return marketcalendar.TradingCalendar{}, nil, stockCalendarErr
	}
	return stockCalendarValue, stockCalendarLocation, nil
}

// barsDuration 返回 n 根定长 bar 的总时长；超出可表示范围时报错，而不是溢出成一个错误的时间。
func barsDuration(n int, duration time.Duration) (time.Duration, error) {
	if n < 0 || duration <= 0 || int64(n) > math.MaxInt64/int64(duration) {
		return 0, fmt.Errorf("%d 根 %s 的 bar 超出可表示的时间范围", n, duration)
	}
	return time.Duration(n) * duration, nil
}

func parseBarDuration(bar string) (time.Duration, error) {
	if len(bar) < 2 {
		return 0, fmt.Errorf("bar %q 无效", bar)
	}
	n, err := strconv.Atoi(bar[:len(bar)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bar %q 无效", bar)
	}
	switch bar[len(bar)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("bar %q 无效", bar)
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func normalizeCalendar(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
