// Package period 负责策略周期、交易日历、换仓时钟与区间换算（纯计算，无 IO）。
package period

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	// 内嵌时区数据：主机缺少 tzdata 时各市场的时区（上海、香港、纽约）仍然可用。
	_ "time/tzdata"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// ErrUnsupportedCalendar 表示策略日历与频率的组合不受支持。
var ErrUnsupportedCalendar = errors.New("不支持的策略日历")

// DefaultCalendar 是没有声明日历的数据集使用的日历。
const DefaultCalendar = "crypto_24x7"

// StockTradingDaysPerYear 是 A 股一年的交易日数（与回放年化口径一致），用于把自然时长折算为交易日根数。
// 港股约 246 天、美股约 252 天，折算与年化都只是估算，共用这个数。
const StockTradingDaysPerYear = 244

// describedError 是换成中文说明的错误：Error 只给说明，原错误（可能是英文）经 Unwrap 留给日志与 errors.Is 判断。
type describedError struct {
	message string
	cause   error
}

func (e *describedError) Error() string { return e.message }
func (e *describedError) Unwrap() error { return e.cause }

// PeriodBoundaries 是一根已闭合 bar 在日历上的标识：Storage 用 StorageStart（bar_start）作行键，
// Strategy 与 Trade 用 BarEnd。PreviousStart 与 NextEnd 按有效 bar 推进，而不是按墙钟时长。
// 股票市场内嵌日历的第一个交易日没有上一根，PreviousStart 为零值；最后一个交易日没有下一根，NextEnd 为零值。
type PeriodBoundaries struct {
	StorageStart  time.Time
	PreviousStart time.Time
	BarEnd        time.Time
	NextEnd       time.Time
	BarIndex      int64
}

// 股票市场内嵌日历的可用截止日检查结果（StockCalendarReadiness 的底层原因）。
var (
	ErrCalendarExpiring = errors.New("交易日历即将到期")
	ErrCalendarExpired  = errors.New("交易日历已到期")
)

// translateCalendarError 把交易日历包的错误换成带市场名与覆盖范围的说明；nil 与已换过的错误原样返回。
// market 为 nil（说不清是哪个市场）时不写市场名与范围。
func translateCalendarError(market *sessionMarket, err error) error {
	if err == nil {
		return nil
	}
	var translated *describedError
	if errors.As(err, &translated) {
		return err
	}
	first, last := "未知", "未知"
	if market != nil {
		first, last = market.calendar.FirstDate().String(), market.calendar.LastDate().String()
	}
	name, spaced := market.name(), market.spacedName()
	message := name + "交易日历计算失败"
	switch {
	case errors.Is(err, marketcalendar.ErrNotTradingDay):
		message = "不是" + spaced + "交易日"
	case errors.Is(err, marketcalendar.ErrNoNextTradingDay):
		message = fmt.Sprintf("超出%s内嵌交易日历的范围（%s 至 %s），之后的交易日未知，请更新日历数据", spaced, first, last)
	case errors.Is(err, marketcalendar.ErrNoPreviousTradingDay):
		message = fmt.Sprintf("之前没有交易日（%s内嵌交易日历起点为 %s）", name, first)
	case errors.Is(err, marketcalendar.ErrOutOfCoverage):
		message = fmt.Sprintf("超出%s内嵌交易日历的范围（%s 至 %s）", spaced, first, last)
	case errors.Is(err, marketcalendar.ErrInvalidCivilDate):
		message = name + "交易日历的日期无效"
	}
	return &describedError{message: message, cause: err}
}

// stockDayError 给交易日历错误加上所涉市场与日期的中文上下文。
func stockDayError(market *sessionMarket, day marketcalendar.CivilDate, err error) error {
	translated := translateCalendarError(market, err)
	return &describedError{message: fmt.Sprintf("%s日期 %s：%s", market.name(), day, translated.Error()), cause: err}
}

// periodClock 是一种“日历 + 周期”组合的周期换算。对外的周期函数（ClosedPeriod、FromStorageStart 等）只做参数
// 规范化与提前返回，具体换算都委托给它。三种实现：crypto 定长（fixedClock）、股票市场日线（stockDailyClock，bar_start
// 是交易日期的 UTC 零点）、股票市场日内（stockSessionClock，按 barclock 的交易时段分桶，午休、隔夜、休市不占根数）。
// 股票市场（A 股、港股、美股）按日历 ID 从 barclock 的市场表查，见 sessionMarketFor。
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
	if calendarID == DefaultCalendar {
		duration, err := ParseBarDuration(frequency)
		if err != nil {
			return nil, err
		}
		return fixedClock{duration: duration}, nil
	}
	market, ok, err := sessionMarketFor(calendarID)
	switch {
	case err != nil:
		return nil, err
	case ok && frequency == "1d":
		return stockDailyClock{market: market, calendar: market.calendar, location: market.location}, nil
	case ok:
		return stockSessionClockFor(market, frequency)
	}
	return nil, fmt.Errorf("%w：日历 %q 不支持频率 %q", ErrUnsupportedCalendar, calendarID, frequency)
}

// ClosedPeriod 返回 trigger 时刻最近一根已闭合的 bar。股票市场日线的 bar_start 是交易日期的 UTC 零点（日期键），
// 策略边界是当地的收盘时刻（A 股是上海 15:00，半日市是当天实际的收市时刻），日历负责在两者之间换算并跳过周末与节假日；
// 日内周期跳过午休、隔夜与休市。
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

// HistoryStart 返回以 storageStart 为最后一根、共 count 根 bar 的第一根 bar_start；股票市场按有效 bar 回退。
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

// FloorStorageStart 把一个 Storage 行键规整为不晚于它的周期起点：股票市场取不晚于它的交易日（日内取不晚于它的交易时段 bar），
// 并夹到内嵌日历的最后一个交易日之内（索引里可能有误写在节假日或日历之外的行）；crypto 原样返回。用于映射索引统计的最新一根。
func FloorStorageStart(calendarID, frequency string, at time.Time) (time.Time, error) {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return time.Time{}, err
	}
	return clock.floor(at)
}

// CeilStorageStart 把一个 Storage 行键规整为不早于它的周期起点：股票市场取不早于它的交易日（日内取不早于它的交易时段 bar）；
// crypto 原样返回。用于映射索引统计的最早一根。
func CeilStorageStart(calendarID, frequency string, at time.Time) (time.Time, error) {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return time.Time{}, err
	}
	return clock.ceil(at)
}

// TradingDayBars 返回日历与周期组合每个完整交易日的根数：股票市场日线为 1，日内按交易时段折算（A 股 5m 为 48，
// 港股 5m 为 66）；crypto 等连续交易的日历与不受支持的组合返回 0。半日市当天的根数更少，这个数只用于估算
// （预热期数、保留根数、年化），要精确到某一天的换算走交易序号（见 stockAxis）。
func TradingDayBars(calendarID, frequency string) int {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return 0
	}
	return clock.tradingDayBars()
}

// WarmClock 返回估算预热期数用的每日根数（见 dsl.BarClock）：股票市场取交易日的根数，加密取一天的根数（周期长于一天时按 1）。
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

// stockDailyClock 是股票市场的日线：bar_start 是日期键（交易日期的 UTC 零点，与 barclock 的 1d 键、采集写入的行键一致），
// bar_end 是当日当地的收盘时刻（A 股是上海 15:00；半日市是当天实际的收市时刻），BarIndex 是交易日序号。日期键只表示日期，
// 不是一个真实时刻：由键得日期按 UTC 取年月日，由 bar_end 或触发时刻得日期才按市场的时区。
type stockDailyClock struct {
	market   *sessionMarket
	calendar marketcalendar.TradingCalendar
	location *time.Location
}

func (c stockDailyClock) closed(trigger time.Time) (PeriodBoundaries, error) {
	local := trigger.In(c.location)
	day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return PeriodBoundaries{}, translateCalendarError(c.market, err)
	}
	closeAt := c.market.closeAt(day)
	closed := day
	if local.Before(closeAt) {
		closed, err = c.calendar.PrevTradingDay(day)
	} else if status, statusErr := c.calendar.Status(day); statusErr != nil {
		return PeriodBoundaries{}, stockDayError(c.market, day, statusErr)
	} else if status != marketcalendar.TradingDay {
		closed, err = c.calendar.PrevTradingDay(day)
	}
	if err != nil {
		return PeriodBoundaries{}, stockDayError(c.market, day, err)
	}
	return boundariesForStockDay(c.market, closed)
}

func (c stockDailyClock) fromStorageStart(storageStart time.Time) (PeriodBoundaries, error) {
	if err := CheckStockRowKey(storageStart); err != nil {
		return PeriodBoundaries{}, err
	}
	day, err := stockKeyDay(storageStart)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	status, err := c.calendar.Status(day)
	if err != nil {
		return PeriodBoundaries{}, stockDayError(c.market, day, err)
	}
	if status != marketcalendar.TradingDay {
		return PeriodBoundaries{}, fmt.Errorf("Storage 周期 %s 不是%s交易日", day, c.market.spacedName())
	}
	return boundariesForStockDay(c.market, day)
}

func (c stockDailyClock) fromBarEnd(end time.Time) (PeriodBoundaries, error) {
	day, err := localDay(end, c.location)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	if status, err := c.calendar.Status(day); err != nil {
		return PeriodBoundaries{}, stockDayError(c.market, day, err)
	} else if status != marketcalendar.TradingDay {
		return PeriodBoundaries{}, fmt.Errorf("bar_end %s 不在%s交易日上", c.market.label(end), c.market.spacedName())
	}
	return boundariesForStockDay(c.market, day)
}

// boundariesForStockDay 计算交易日 day 的周期边界。上一根与下一根只在回看或推进 bar 时需要：日历的第一个交易日
// 没有上一根、最后一个交易日没有下一根，对应字段留空，而不是让这一根本身也无法处理。
func boundariesForStockDay(market *sessionMarket, day marketcalendar.CivilDate) (PeriodBoundaries, error) {
	calendar := market.calendar
	index, err := calendar.TradingDayIndex(day)
	if err != nil {
		return PeriodBoundaries{}, stockDayError(market, day, err)
	}
	boundaries := PeriodBoundaries{
		StorageStart: stockDayKey(day),
		BarEnd:       market.closeAt(day).UTC(),
		BarIndex:     int64(index),
	}
	previous, err := calendar.PrevTradingDay(day)
	switch {
	case err == nil:
		boundaries.PreviousStart = stockDayKey(previous)
	case !errors.Is(err, marketcalendar.ErrNoPreviousTradingDay):
		return PeriodBoundaries{}, stockDayError(market, day, err)
	}
	next, err := calendar.NextTradingDay(day)
	switch {
	case err == nil:
		boundaries.NextEnd = market.closeAt(next).UTC()
	case !errors.Is(err, marketcalendar.ErrNoNextTradingDay):
		return PeriodBoundaries{}, stockDayError(market, day, err)
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
			return time.Time{}, stockDayError(c.market, day, err)
		}
		day = moved
	}
	return c.market.closeAt(day).UTC(), nil
}

func (c stockDailyClock) historyStart(last PeriodBoundaries, count int) (time.Time, error) {
	day, err := stockKeyDay(last.StorageStart)
	if err != nil {
		return time.Time{}, err
	}
	for i := 1; i < count; i++ {
		previous, err := c.calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(c.market, day, err)
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
		return time.Time{}, stockDayError(c.market, day, marketcalendar.ErrOutOfCoverage)
	}
	if status, err := c.calendar.Status(day); err != nil {
		return time.Time{}, stockDayError(c.market, day, err)
	} else if status != marketcalendar.TradingDay {
		previous, err := c.calendar.PrevTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(c.market, day, err)
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
		return time.Time{}, stockDayError(c.market, day, err)
	} else if status != marketcalendar.TradingDay {
		next, err := c.calendar.NextTradingDay(day)
		if err != nil {
			return time.Time{}, stockDayError(c.market, day, err)
		}
		day = next
	}
	return stockDayKey(day), nil
}

func (c stockDailyClock) tradingDayBars() int { return 1 }

// stockDayKey 返回交易日 day 的股票市场日线行键：交易日期的 UTC 零点。
func stockDayKey(day marketcalendar.CivilDate) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
}

// stockKeyDay 返回股票市场日线行键（或要规整成行键的时刻）所在的日期：按 UTC 取年月日。
func stockKeyDay(at time.Time) (marketcalendar.CivilDate, error) {
	return localDay(at, time.UTC)
}

// checkStockRowKey 要求股票市场日线的行键是 UTC 零点（日期键）。不静默规整：不按日期键写行的数据集（例如按上海零点，
// 即前一日 16:00 UTC）规整后每期都读不到行，只会一直 no_data（回放则每根都缺数据）。
func CheckStockRowKey(at time.Time) error {
	if utc := at.UTC(); !utc.Equal(time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)) {
		return fmt.Errorf("行键 %s 不是 UTC 零点：股票市场日线的 bar_start 必须是交易日期的 UTC 零点（例如 2026-10-09T00:00:00Z）", utc.Format(time.RFC3339))
	}
	return nil
}

// localDay 返回时刻在给定时区的日期。
func localDay(at time.Time, location *time.Location) (marketcalendar.CivilDate, error) {
	local := at.In(location)
	day, err := marketcalendar.NewCivilDate(local.Year(), local.Month(), local.Day())
	if err != nil {
		return marketcalendar.CivilDate{}, translateCalendarError(nil, err)
	}
	return day, nil
}

// CheckCalendar 校验日历与周期的组合受支持：crypto 支持定长的分钟、小时、日线周期；股票市场支持日线与 barclock 允许的
// 日内周期（能整除每个交易时段），且内嵌日历可用。不依赖任何具体日期，避免拿一个恰好不是交易日的日期去探测。
func CheckCalendar(calendarID, frequency string) error {
	_, err := clockFor(calendarID, frequency)
	return err
}

// StockCalendarReadiness 检查日历 calendarID 的内嵌交易日历在 now 是否还能完整处理当天的周期；不是股票市场的日历返回 nil。周期的有效期要推到其后 validBars 个
// 交易日，日历最后 validBars 个交易日的周期已推不出有效期，实际可用截止日是“最后一个交易日往前 validBars 个交易日”：
// 过了它返回 ErrCalendarExpired，warning 时间内将到它返回 ErrCalendarExpiring。按日线口径计算，对日内周期偏保守
// （validBars 根日内 bar 用不了 validBars 个交易日），不按周期区分。
func StockCalendarReadiness(calendarID string, now time.Time, warning time.Duration, validBars int) error {
	market, ok, err := sessionMarketFor(calendarID)
	if err != nil || !ok {
		return err
	}
	calendar, location := market.calendar, market.location
	// 日历的最后一天就是最后一个交易日（日历只列交易日）。
	usable := calendar.LastDate()
	for i := 0; i < validBars; i++ {
		previous, err := calendar.PrevTradingDay(usable)
		if err != nil {
			return stockDayError(market, usable, err)
		}
		usable = previous
	}
	today, err := localDay(now, location)
	if err != nil {
		return err
	}
	if today.After(usable) {
		return &describedError{
			message: fmt.Sprintf("%s内嵌交易日历止于 %s，%s 之后的周期已无法推算有效期，请更新日历数据", market.name(), calendar.LastDate(), usable),
			cause:   ErrCalendarExpired,
		}
	}
	usableEnd := market.closeAt(usable)
	if warning > 0 && !now.Add(warning).Before(usableEnd) {
		return &describedError{
			message: fmt.Sprintf("%s内嵌交易日历止于 %s，%s 之后的周期将无法处理，请在此之前更新日历数据", market.name(), calendar.LastDate(), usable),
			cause:   ErrCalendarExpiring,
		}
	}
	return nil
}

// barsDuration 返回 n 根定长 bar 的总时长；超出可表示范围时报错，而不是溢出成一个错误的时间。
func barsDuration(n int, duration time.Duration) (time.Duration, error) {
	if n < 0 || duration <= 0 || int64(n) > math.MaxInt64/int64(duration) {
		return 0, fmt.Errorf("%d 根 %s 的 bar 超出可表示的时间范围", n, duration)
	}
	return time.Duration(n) * duration, nil
}

func ParseBarDuration(bar string) (time.Duration, error) {
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

// stockDaily 报告日历与周期是否是股票市场的日线。
func StockDaily(calendarID, frequency string) bool {
	return strings.ToLower(strings.TrimSpace(frequency)) == "1d" && SessionCalendar(calendarID)
}

// RetentionBars 把数据集保留期（"<n>h" 或 "forever"）换算为根数；A 股先按交易日折算，再乘每个交易日的根数（日线 1，
// 5m 为 48）。forever 或无法解析返回 false。
func RetentionBars(retention, calendar, bar string) (int, bool) {
	retention = strings.TrimSpace(strings.ToLower(retention))
	if retention == "" || retention == "forever" || !strings.HasSuffix(retention, "h") {
		return 0, false
	}
	hours, err := strconv.Atoi(strings.TrimSuffix(retention, "h"))
	if err != nil || hours <= 0 {
		return 0, false
	}
	if perDay := TradingDayBars(calendar, bar); perDay > 0 {
		return hours / 24 * StockTradingDaysPerYear / 365 * perDay, true
	}
	duration, err := ParseBarDuration(bar)
	if err != nil || duration <= 0 {
		return 0, false
	}
	return int((time.Duration(hours) * time.Hour) / duration), true
}
