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

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
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

// periodClock 是一种“日历 + 周期”组合的周期换算。对外的周期函数（ClosedPeriod、FromStorageStart 等）只做参数
// 规范化与提前返回，具体换算都委托给它。三种实现：crypto 定长（fixedClock）、A 股日线（stockDailyClock，bar_start
// 是交易日期的 UTC 零点）、A 股日内（stockSessionClock，按 barclock 的交易时段分桶，午休、隔夜、休市不占根数）。
type periodClock interface {
	// closed 返回 trigger 时刻最近一根已闭合的 bar。
	closed(trigger time.Time) (PeriodBoundaries, error)
	// fromStorageStart 把 Storage 行键（bar_start）映射为周期边界，行键不是周期起点时报错。
	fromStorageStart(start time.Time) (PeriodBoundaries, error)
	// fromBarEnd 把 bar_end 映射为周期边界。
	fromBarEnd(end time.Time) (PeriodBoundaries, error)
	// advance 把 bar_end 按有效 bar 移动 n 根（n 不为 0）。
	advance(end time.Time, n int) (time.Time, error)
	// historyStart 返回以 last 为最后一根、共 count 根（count > 1）的第一根 bar_start。
	historyStart(last PeriodBoundaries, count int) (time.Time, error)
	// floor、ceil 把任意时刻规整为不晚于、不早于它的周期起点。
	floor(at time.Time) (time.Time, error)
	ceil(at time.Time) (time.Time, error)
	// tradingDayBars 是每个交易日的根数；连续交易、没有交易日概念的日历返回 0。
	tradingDayBars() int
}

// clockFor 返回日历与周期组合的周期时钟；组合不受支持时返回 ErrUnsupportedCalendar（crypto 的周期写法无效时返回解析错误）。
func clockFor(calendarID, frequency string) (periodClock, error) {
	calendarID = normalizeCalendar(calendarID)
	frequency = strings.ToLower(strings.TrimSpace(frequency))
	switch {
	case calendarID == DefaultCalendar:
		duration, err := parseBarDuration(frequency)
		if err != nil {
			return nil, err
		}
		return fixedClock{duration: duration}, nil
	case calendarID == "cn_stock" && frequency == "1d":
		calendar, location, err := stockCalendar()
		if err != nil {
			return nil, err
		}
		return stockDailyClock{calendar: calendar, location: location}, nil
	case calendarID == "cn_stock":
		return stockSessionClockFor(frequency)
	}
	return nil, fmt.Errorf("%w：日历 %q 不支持频率 %q", ErrUnsupportedCalendar, calendarID, frequency)
}

// ClosedPeriod 返回 trigger 时刻最近一根已闭合的 bar。A 股日线的 bar_start 是交易日期的 UTC 零点（日期键），
// 策略边界是上海 15:00 收盘，日历负责在两者之间换算并跳过周末与节假日；A 股日内跳过午休、隔夜与休市。
func ClosedPeriod(calendarID, frequency string, trigger time.Time) (PeriodBoundaries, error) {
	if trigger.IsZero() {
		return PeriodBoundaries{}, errors.New("缺少触发时间")
	}
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	return clock.closed(trigger)
}

// FromStorageStart 把 Storage 的行键（bar_start）映射为策略使用的周期边界。
func FromStorageStart(calendarID, frequency string, storageStart time.Time) (PeriodBoundaries, error) {
	if storageStart.IsZero() {
		return PeriodBoundaries{}, errors.New("缺少 Storage 周期时间")
	}
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	return clock.fromStorageStart(storageStart)
}

// FromBarEnd 把 bar_end 映射为周期边界。
func FromBarEnd(calendarID, frequency string, end time.Time) (PeriodBoundaries, error) {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	return clock.fromBarEnd(end)
}

// AdvanceBarEnd 按有效 bar 移动 bar_end：n 为负表示向前回溯，0 原样返回。
func AdvanceBarEnd(calendarID, frequency string, end time.Time, n int) (time.Time, error) {
	if n == 0 {
		return end.UTC(), nil
	}
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return time.Time{}, err
	}
	return clock.advance(end, n)
}

// PreviousStorageStart 返回上一根 bar 的 bar_start。
func PreviousStorageStart(calendarID, frequency string, storageStart time.Time) (time.Time, error) {
	period, err := FromStorageStart(calendarID, frequency, storageStart)
	if err != nil {
		return time.Time{}, err
	}
	return period.PreviousStart, nil
}

// HistoryStart 返回以 storageStart 为最后一根、共 count 根 bar 的第一根 bar_start；A 股按有效 bar 回退。
func HistoryStart(calendarID, frequency string, storageStart time.Time, count int) (time.Time, error) {
	if count <= 1 {
		return storageStart.UTC(), nil
	}
	if storageStart.IsZero() {
		return time.Time{}, errors.New("缺少 Storage 周期时间")
	}
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return time.Time{}, err
	}
	period, err := clock.fromStorageStart(storageStart)
	if err != nil {
		return time.Time{}, err
	}
	return clock.historyStart(period, count)
}

// FloorStorageStart 把一个 Storage 行键规整为不晚于它的周期起点：A 股取不晚于它的交易日（日内取不晚于它的交易时段 bar），
// 并夹到内嵌日历的最后一个交易日之内（索引里可能有误写在节假日或日历之外的行）；crypto 原样返回。用于映射索引统计的最新一根。
func FloorStorageStart(calendarID, frequency string, at time.Time) (time.Time, error) {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return time.Time{}, err
	}
	return clock.floor(at)
}

// CeilStorageStart 把一个 Storage 行键规整为不早于它的周期起点：A 股取不早于它的交易日（日内取不早于它的交易时段 bar）；
// crypto 原样返回。用于映射索引统计的最早一根。
func CeilStorageStart(calendarID, frequency string, at time.Time) (time.Time, error) {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return time.Time{}, err
	}
	return clock.ceil(at)
}

// TradingDayBars 返回日历与周期组合每个交易日的根数：A 股日线为 1，A 股日内按交易时段折算（5m 为 48）；
// crypto 等连续交易的日历与不受支持的组合返回 0。
func TradingDayBars(calendarID, frequency string) int {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return 0
	}
	return clock.tradingDayBars()
}

// WarmClock 返回估算预热期数用的每日根数（见 dsl.BarClock）：A 股取交易日的根数，加密取一天的根数（周期长于一天时按 1）。
func WarmClock(calendarID, frequency string) dsl.BarClock {
	if perDay := TradingDayBars(calendarID, frequency); perDay > 0 {
		return dsl.BarClock{PerDay: perDay, Stock: true}
	}
	if clock, err := clockFor(calendarID, frequency); err == nil {
		if fixed, ok := clock.(fixedClock); ok && fixed.duration > 0 && fixed.duration < 24*time.Hour {
			return dsl.BarClock{PerDay: int(24 * time.Hour / fixed.duration)}
		}
	}
	return dsl.BarClock{PerDay: 1}
}

// fixedClock 是 crypto 24x7 的定长周期：bar 按 Unix 纪元对齐。
type fixedClock struct{ duration time.Duration }

func (c fixedClock) boundaries(end time.Time) PeriodBoundaries {
	start := end.Add(-c.duration)
	return PeriodBoundaries{StorageStart: start, PreviousStart: start.Add(-c.duration), BarEnd: end, NextEnd: end.Add(c.duration), BarIndex: end.UnixNano() / c.duration.Nanoseconds()}
}

func (c fixedClock) closed(trigger time.Time) (PeriodBoundaries, error) {
	// 最近闭合的 bar_end 是 trigger 向下取整到 bar 时长的整数倍。
	end := time.Unix(0, (trigger.UTC().UnixNano()/c.duration.Nanoseconds())*c.duration.Nanoseconds()).UTC()
	return c.boundaries(end), nil
}

func (c fixedClock) fromStorageStart(start time.Time) (PeriodBoundaries, error) {
	return c.boundaries(start.UTC().Add(c.duration)), nil
}

func (c fixedClock) fromBarEnd(end time.Time) (PeriodBoundaries, error) {
	return c.boundaries(end.UTC()), nil
}

func (c fixedClock) advance(end time.Time, n int) (time.Time, error) {
	shift, err := barsDuration(absInt(n), c.duration)
	if err != nil {
		return time.Time{}, err
	}
	if n < 0 {
		shift = -shift
	}
	return end.UTC().Add(shift), nil
}

func (c fixedClock) historyStart(last PeriodBoundaries, count int) (time.Time, error) {
	shift, err := barsDuration(count-1, c.duration)
	if err != nil {
		return time.Time{}, err
	}
	return last.StorageStart.Add(-shift), nil
}

func (c fixedClock) floor(at time.Time) (time.Time, error) { return at.UTC(), nil }
func (c fixedClock) ceil(at time.Time) (time.Time, error)  { return at.UTC(), nil }
func (c fixedClock) tradingDayBars() int                   { return 0 }

// stockDailyClock 是 A 股日线：bar_start 是日期键（交易日期的 UTC 零点，与 barclock 的 1d 键、采集写入的行键一致），
// bar_end 是当日上海 15:00 收盘，BarIndex 是交易日序号。日期键只表示日期，不是一个真实时刻：由键得日期按 UTC 取年月日，
// 由 bar_end 或触发时刻得日期才按上海时区。
type stockDailyClock struct {
	calendar marketcalendar.TradingCalendar
	location *time.Location
}

func (c stockDailyClock) closed(trigger time.Time) (PeriodBoundaries, error) {
	local := trigger.In(c.location)
	day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return PeriodBoundaries{}, translateCalendarError(err)
	}
	closeAt := time.Date(local.Year(), local.Month(), local.Day(), 15, 0, 0, 0, c.location)
	closed := day
	if local.Before(closeAt) {
		closed, err = c.calendar.PrevTradingDay(day)
	} else if status, statusErr := c.calendar.Status(day); statusErr != nil {
		return PeriodBoundaries{}, stockDayError(day, statusErr)
	} else if status != marketcalendar.TradingDay {
		closed, err = c.calendar.PrevTradingDay(day)
	}
	if err != nil {
		return PeriodBoundaries{}, stockDayError(day, err)
	}
	return boundariesForStockDay(c.calendar, c.location, closed)
}

func (c stockDailyClock) fromStorageStart(storageStart time.Time) (PeriodBoundaries, error) {
	if err := checkStockRowKey(storageStart); err != nil {
		return PeriodBoundaries{}, err
	}
	day, err := stockKeyDay(storageStart)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	status, err := c.calendar.Status(day)
	if err != nil {
		return PeriodBoundaries{}, stockDayError(day, err)
	}
	if status != marketcalendar.TradingDay {
		return PeriodBoundaries{}, fmt.Errorf("Storage 周期 %s 不是 A 股交易日", day)
	}
	return boundariesForStockDay(c.calendar, c.location, day)
}

func (c stockDailyClock) fromBarEnd(end time.Time) (PeriodBoundaries, error) {
	day, err := localDay(end, c.location)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	if status, err := c.calendar.Status(day); err != nil {
		return PeriodBoundaries{}, stockDayError(day, err)
	} else if status != marketcalendar.TradingDay {
		return PeriodBoundaries{}, fmt.Errorf("bar_end %s 不在 A 股交易日上", shanghaiLabel(end, c.location))
	}
	return boundariesForStockDay(c.calendar, c.location, day)
}

// boundariesForStockDay 计算交易日 day 的周期边界。上一根与下一根只在回看或推进 bar 时需要：日历的第一个交易日
// 没有上一根、最后一个交易日没有下一根，对应字段留空，而不是让这一根本身也无法处理。
func boundariesForStockDay(calendar marketcalendar.TradingCalendar, location *time.Location, day marketcalendar.CivilDate) (PeriodBoundaries, error) {
	index, err := calendar.TradingDayIndex(day)
	if err != nil {
		return PeriodBoundaries{}, stockDayError(day, err)
	}
	boundaries := PeriodBoundaries{
		StorageStart: stockDayKey(day),
		BarEnd:       time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, location).UTC(),
		BarIndex:     int64(index),
	}
	previous, err := calendar.PrevTradingDay(day)
	switch {
	case err == nil:
		boundaries.PreviousStart = stockDayKey(previous)
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

func (c stockDailyClock) advance(end time.Time, n int) (time.Time, error) {
	day, err := localDay(end, c.location)
	if err != nil {
		return time.Time{}, err
	}
	for i := 0; i < absInt(n); i++ {
		var moved marketcalendar.CivilDate
		if n > 0 {
			moved, err = c.calendar.NextTradingDay(day)
		} else {
			moved, err = c.calendar.PrevTradingDay(day)
		}
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = moved
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 15, 0, 0, 0, c.location).UTC(), nil
}

func (c stockDailyClock) historyStart(last PeriodBoundaries, count int) (time.Time, error) {
	day, err := stockKeyDay(last.StorageStart)
	if err != nil {
		return time.Time{}, err
	}
	for i := 1; i < count; i++ {
		previous, err := c.calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = previous
	}
	return stockDayKey(day), nil
}

func (c stockDailyClock) floor(at time.Time) (time.Time, error) {
	day, err := stockKeyDay(at)
	if err != nil {
		return time.Time{}, err
	}
	if day.After(c.calendar.LastDate()) {
		day = c.calendar.LastDate()
	}
	if day.Before(c.calendar.FirstDate()) {
		return time.Time{}, stockDayError(day, marketcalendar.ErrOutOfCoverage)
	}
	if status, err := c.calendar.Status(day); err != nil {
		return time.Time{}, stockDayError(day, err)
	} else if status != marketcalendar.TradingDay {
		previous, err := c.calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = previous
	}
	return stockDayKey(day), nil
}

func (c stockDailyClock) ceil(at time.Time) (time.Time, error) {
	day, err := stockKeyDay(at)
	if err != nil {
		return time.Time{}, err
	}
	if day.Before(c.calendar.FirstDate()) {
		day = c.calendar.FirstDate()
	}
	if status, err := c.calendar.Status(day); err != nil {
		return time.Time{}, stockDayError(day, err)
	} else if status != marketcalendar.TradingDay {
		next, err := c.calendar.NextTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(day, err)
		}
		day = next
	}
	return stockDayKey(day), nil
}

func (c stockDailyClock) tradingDayBars() int { return 1 }

// stockDayKey 返回交易日 day 的 A 股日线行键：交易日期的 UTC 零点。
func stockDayKey(day marketcalendar.CivilDate) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
}

// stockKeyDay 返回 A 股日线行键（或要规整成行键的时刻）所在的日期：按 UTC 取年月日。
func stockKeyDay(at time.Time) (marketcalendar.CivilDate, error) {
	return localDay(at, time.UTC)
}

// checkStockRowKey 要求 A 股日线的行键是 UTC 零点（日期键）。不静默规整：不按日期键写行的数据集（例如按上海零点，
// 即前一日 16:00 UTC）规整后每期都读不到行，只会一直 no_data（回放则每根都缺数据）。
func checkStockRowKey(at time.Time) error {
	if utc := at.UTC(); !utc.Equal(time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)) {
		return fmt.Errorf("行键 %s 不是 UTC 零点：A 股日线的 bar_start 必须是交易日期的 UTC 零点（例如 2026-10-09T00:00:00Z）", utc.Format(time.RFC3339))
	}
	return nil
}

// shanghaiLabel 把时刻写成上海时间（精确到秒），给 A 股的报错用。
func shanghaiLabel(at time.Time, location *time.Location) string {
	return at.In(location).Format("2006-01-02 15:04:05") + " 上海时间"
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

// CheckCalendar 校验日历与周期的组合受支持：crypto 支持定长的分钟、小时、日线周期；A 股支持日线与 barclock 允许的
// 日内周期（能整除每个交易时段），且内嵌日历可用。不依赖任何具体日期，避免拿一个恰好不是交易日的日期去探测。
func CheckCalendar(calendarID, frequency string) error {
	_, err := clockFor(calendarID, frequency)
	return err
}

// StockCalendarReadiness 检查内嵌 A 股日历在 now 是否还能完整处理当天的周期。周期的有效期要推到其后 validBars 个
// 交易日，日历最后 validBars 个交易日的周期已推不出有效期，实际可用截止日是“最后一个交易日往前 validBars 个交易日”：
// 过了它返回 ErrCalendarExpired，warning 时间内将到它返回 ErrCalendarExpiring。按日线口径计算，对日内周期偏保守
// （validBars 根日内 bar 用不了 validBars 个交易日），不按周期区分。
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
