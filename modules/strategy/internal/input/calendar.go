package input

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	// 内嵌时区数据：主机缺少 tzdata 时 A 股的上海时区仍然可用。
	_ "time/tzdata"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// ErrUnsupportedCalendar 表示策略日历与频率的组合不受支持。
var ErrUnsupportedCalendar = errors.New("不支持的策略日历")

// PeriodBoundaries 是一根已闭合 bar 在日历上的标识：Storage 用 StorageStart（bar_start）作行键，
// Strategy 与 Trade 用 BarEnd。PreviousStart 与 NextEnd 按有效 bar 推进，而不是按墙钟时长。
// A 股内嵌日历的第一个交易日没有上一根，PreviousStart 为零值；最后一个交易日没有下一根，NextEnd 为零值。
type PeriodBoundaries struct {
	StorageStart  time.Time
	PreviousStart time.Time
	BarEnd        time.Time
	NextEnd       time.Time
	BarIndex      int64
}

// A 股内嵌日历的可用截止日检查结果（StockCalendarReadiness 的底层原因）。
var (
	ErrCalendarExpiring = errors.New("A 股交易日历即将到期")
	ErrCalendarExpired  = errors.New("A 股交易日历已到期")
)

// translateCalendarError 把交易日历包的错误换成带覆盖范围的说明；nil 与已换过的错误原样返回。
func translateCalendarError(err error) error {
	if err == nil {
		return nil
	}
	var translated *describedError
	if errors.As(err, &translated) {
		return err
	}
	first, last := "未知", "未知"
	if calendar, _, loadErr := stockCalendar(); loadErr == nil {
		first, last = calendar.FirstDate().String(), calendar.LastDate().String()
	}
	message := "A 股交易日历计算失败"
	switch {
	case errors.Is(err, marketcalendar.ErrNotTradingDay):
		message = "不是 A 股交易日"
	case errors.Is(err, marketcalendar.ErrNoNextTradingDay):
		message = fmt.Sprintf("超出 A 股内嵌交易日历的范围（%s 至 %s），之后的交易日未知，请更新日历数据", first, last)
	case errors.Is(err, marketcalendar.ErrNoPreviousTradingDay):
		message = fmt.Sprintf("之前没有交易日（A 股内嵌交易日历起点为 %s）", first)
	case errors.Is(err, marketcalendar.ErrOutOfCoverage):
		message = fmt.Sprintf("超出 A 股内嵌交易日历的范围（%s 至 %s）", first, last)
	case errors.Is(err, marketcalendar.ErrInvalidCivilDate):
		message = "A 股交易日历的日期无效"
	}
	return &describedError{message: message, cause: err}
}

// stockDayError 给交易日历错误加上所涉日期的中文上下文。
func stockDayError(day marketcalendar.CivilDate, err error) error {
	translated := translateCalendarError(err)
	return &describedError{message: fmt.Sprintf("A 股日期 %s：%s", day, translated.Error()), cause: err}
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
			return PeriodBoundaries{}, translateCalendarError(err)
		}
		closeAt := time.Date(local.Year(), local.Month(), local.Day(), 15, 0, 0, 0, location)
		closed := day
		if local.Before(closeAt) {
			closed, err = calendar.PrevTradingDay(day)
		} else if status, statusErr := calendar.Status(day); statusErr != nil {
			return PeriodBoundaries{}, stockDayError(day, statusErr)
		} else if status != marketcalendar.TradingDay {
			closed, err = calendar.PrevTradingDay(day)
		}
		if err != nil {
			return PeriodBoundaries{}, stockDayError(day, err)
		}
		return boundariesForStockDay(calendar, location, closed)
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
		day, err := localDay(storageStart, location)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		if err := checkStockRowKey(storageStart, day, location); err != nil {
			return PeriodBoundaries{}, err
		}
		status, err := calendar.Status(day)
		if err != nil {
			return PeriodBoundaries{}, stockDayError(day, err)
		}
		if status != marketcalendar.TradingDay {
			return PeriodBoundaries{}, fmt.Errorf("Storage 周期 %s 不是 A 股交易日", day)
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
		day, err := localDay(end, location)
		if err != nil {
			return PeriodBoundaries{}, err
		}
		if status, err := calendar.Status(day); err != nil {
			return PeriodBoundaries{}, stockDayError(day, err)
		} else if status != marketcalendar.TradingDay {
			return PeriodBoundaries{}, fmt.Errorf("bar_end %s 不在 A 股交易日上", end.UTC().Format(time.RFC3339))
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

// boundariesForStockDay 计算交易日 day 的周期边界。上一根与下一根只在回看或推进 bar 时需要：日历的第一个交易日
// 没有上一根、最后一个交易日没有下一根，对应字段留空，而不是让这一根本身也无法处理。
func boundariesForStockDay(calendar marketcalendar.TradingCalendar, location *time.Location, day marketcalendar.CivilDate) (PeriodBoundaries, error) {
	index, err := calendar.TradingDayIndex(day)
	if err != nil {
		return PeriodBoundaries{}, stockDayError(day, err)
	}
	boundaries := PeriodBoundaries{
		StorageStart: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC(),
		BarEnd:       time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, location).UTC(),
		BarIndex:     int64(index),
	}
	previous, err := calendar.PrevTradingDay(day)
	switch {
	case err == nil:
		boundaries.PreviousStart = time.Date(previous.Year(), previous.Month(), previous.Day(), 0, 0, 0, 0, location).UTC()
	case !errors.Is(err, marketcalendar.ErrNoPreviousTradingDay):
		return PeriodBoundaries{}, stockDayError(day, err)
	}
	next, err := calendar.NextTradingDay(day)
	switch {
	case err == nil:
		boundaries.NextEnd = time.Date(next.Year(), next.Month(), next.Day(), 15, 0, 0, 0, location).UTC()
	case !errors.Is(err, marketcalendar.ErrNoNextTradingDay):
		return PeriodBoundaries{}, stockDayError(day, err)
	}
	return boundaries, nil
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
	day, err := localDay(end, location)
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i < absInt(n); i++ {
		var moved marketcalendar.CivilDate
		if n > 0 {
			moved, err = calendar.NextTradingDay(day)
		} else {
			moved, err = calendar.PrevTradingDay(day)
		}
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = moved
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, location).UTC(), nil
}

// PreviousStorageStart 返回上一根 bar 的 bar_start。
func PreviousStorageStart(calendarID, frequency string, storageStart time.Time) (time.Time, error) {
	period, err := FromStorageStart(calendarID, frequency, storageStart)
	if err != nil {
		return time.Time{}, err
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
	day, err := localDay(period.StorageStart, location)
	if err != nil {
		return time.Time{}, err
	}
	for i := 1; i < count; i++ {
		previous, err := calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = previous
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC(), nil
}

// FloorStorageStart 把一个 Storage 行键规整为不晚于它的周期起点：A 股日线取不晚于它的交易日，并夹到内嵌日历的
// 最后一个交易日之内（索引里可能有误写在节假日或日历之外的行）；crypto 原样返回。用于映射索引统计的最新一根。
func FloorStorageStart(calendarID, frequency string, at time.Time) (time.Time, error) {
	if normalizeCalendar(calendarID) != "cn_stock" || strings.ToLower(strings.TrimSpace(frequency)) != "1d" {
		return at.UTC(), nil
	}
	calendar, location, err := stockCalendar()
	if err != nil {
		return time.Time{}, err
	}
	day, err := localDay(at, location)
	if err != nil {
		return time.Time{}, err
	}
	if day.After(calendar.LastDate()) {
		day = calendar.LastDate()
	}
	if day.Before(calendar.FirstDate()) {
		return time.Time{}, stockDayError(day, marketcalendar.ErrOutOfCoverage)
	}
	if status, err := calendar.Status(day); err != nil {
		return time.Time{}, stockDayError(day, err)
	} else if status != marketcalendar.TradingDay {
		previous, err := calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = previous
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC(), nil
}

// CeilStorageStart 把一个 Storage 行键规整为不早于它的周期起点：A 股日线取不早于它的交易日；crypto 原样返回。
// 用于映射索引统计的最早一根。
func CeilStorageStart(calendarID, frequency string, at time.Time) (time.Time, error) {
	if normalizeCalendar(calendarID) != "cn_stock" || strings.ToLower(strings.TrimSpace(frequency)) != "1d" {
		return at.UTC(), nil
	}
	calendar, location, err := stockCalendar()
	if err != nil {
		return time.Time{}, err
	}
	day, err := localDay(at, location)
	if err != nil {
		return time.Time{}, err
	}
	if day.Before(calendar.FirstDate()) {
		day = calendar.FirstDate()
	}
	if status, err := calendar.Status(day); err != nil {
		return time.Time{}, stockDayError(day, err)
	} else if status != marketcalendar.TradingDay {
		next, err := calendar.NextTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = next
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location).UTC(), nil
}

// checkStockRowKey 要求 A 股日线的行键是 day 的上海时间零点。不静默规整：按 UTC 零点写行却声明了 cn_stock 的数据集，
// 规整后每期都读不到行，只会一直 no_data（回放则每根都缺数据）。
func checkStockRowKey(at time.Time, day marketcalendar.CivilDate, location *time.Location) error {
	if midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location); !at.Equal(midnight) {
		return fmt.Errorf("行键 %s 不是上海时间零点：A 股日线的 bar_start 必须是交易日的上海时间零点（数据集是否按 UTC 零点写行？）", at.UTC().Format(time.RFC3339))
	}
	return nil
}

// localDay 返回时刻在给定时区的日期。
func localDay(at time.Time, location *time.Location) (marketcalendar.CivilDate, error) {
	local := at.In(location)
	day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return marketcalendar.CivilDate{}, translateCalendarError(err)
	}
	return day, nil
}

var (
	stockCalendarOnce     sync.Once
	stockCalendarValue    marketcalendar.TradingCalendar
	stockCalendarLocation *time.Location
	stockCalendarErr      error
)

// CheckCalendar 校验日历与周期的组合受支持：crypto 支持定长的分钟、小时、日线周期，A 股只支持日线且内嵌日历可用。
// 不依赖任何具体日期，避免拿一个恰好不是交易日的日期去探测。
func CheckCalendar(calendarID, frequency string) error {
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.ToLower(strings.TrimSpace(frequency))
	switch {
	case calendarID == DefaultCalendar:
		_, err := parseBarDuration(frequency)
		return err
	case calendarID == "cn_stock" && frequency == "1d":
		_, _, err := stockCalendar()
		return err
	default:
		return fmt.Errorf("%w：日历 %q 不支持频率 %q", ErrUnsupportedCalendar, calendarID, frequency)
	}
}

// StockCalendarReadiness 检查内嵌 A 股日历在 now 是否还能完整处理当天的周期。周期的有效期要推到其后 validBars 个
// 交易日，日历最后 validBars 个交易日的周期已推不出有效期，实际可用截止日是“最后一个交易日往前 validBars 个交易日”：
// 过了它返回 ErrCalendarExpired，warning 时间内将到它返回 ErrCalendarExpiring。
func StockCalendarReadiness(now time.Time, warning time.Duration, validBars int) error {
	calendar, location, err := stockCalendar()
	if err != nil {
		return err
	}
	// 日历的最后一天就是最后一个交易日（日历只列交易日）。
	usable := calendar.LastDate()
	for i := 0; i < validBars; i++ {
		previous, err := calendar.PrevTradingDay(usable)
		if err != nil {
			return stockDayError(usable, err)
		}
		usable = previous
	}
	today, err := localDay(now, location)
	if err != nil {
		return err
	}
	if today.After(usable) {
		return &describedError{
			message: fmt.Sprintf("A 股内嵌交易日历止于 %s，%s 之后的周期已无法推算有效期，请更新日历数据", calendar.LastDate(), usable),
			cause:   ErrCalendarExpired,
		}
	}
	usableEnd := time.Date(usable.Year(), usable.Month(), usable.Day(), 15, 0, 0, 0, location)
	if warning > 0 && !now.Add(warning).Before(usableEnd) {
		return &describedError{
			message: fmt.Sprintf("A 股内嵌交易日历止于 %s，%s 之后的周期将无法处理，请在此之前更新日历数据", calendar.LastDate(), usable),
			cause:   ErrCalendarExpiring,
		}
	}
	return nil
}

// stockCalendar 返回 A 股交易日历与上海时区。内嵌日历的解析与校验较重，进程内只做一次；日历本身不可变。
func stockCalendar() (marketcalendar.TradingCalendar, *time.Location, error) {
	stockCalendarOnce.Do(func() {
		stockCalendarValue, stockCalendarErr = marketcalendar.Load("cn_stock")
		if stockCalendarErr != nil {
			stockCalendarErr = &describedError{message: "A 股内嵌交易日历无法加载", cause: stockCalendarErr}
			return
		}
		stockCalendarLocation, stockCalendarErr = time.LoadLocation("Asia/Shanghai")
		if stockCalendarErr != nil {
			stockCalendarErr = &describedError{message: "无法加载上海时区 Asia/Shanghai", cause: stockCalendarErr}
		}
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

// stockDaily 报告日历与周期是否是 A 股日线。
func stockDaily(calendarID, frequency string) bool {
	return normalizeCalendar(calendarID) == "cn_stock" && strings.ToLower(strings.TrimSpace(frequency)) == "1d"
}
