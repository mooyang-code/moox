package metrics

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

var (
	// ErrCalendarNotCovered 表示要用到的日期超出了交易日历的覆盖范围：不推测是否交易日，由页面显示“日历未覆盖”。
	ErrCalendarNotCovered = errors.New("日历未覆盖")
	// ErrNoTradingCalendar 表示数据所在市场没有可用的交易日历（任务清单没有声明，或声明了未内嵌的日历）。
	ErrNoTradingCalendar = errors.New("没有交易日历")
	// ErrAlignmentUnconfirmed 表示交易所市场上这个周期的 K 线按什么时刻对齐没有可确认的约定：只支持 1 分钟和日线以上，
	// 其余日内周期（5m～4h）既可能按交易时段对齐（09:30/10:30/13:00/14:00），也可能按整点，没有实际数据前不做推断。
	ErrAlignmentUnconfirmed = errors.New("周期对齐未确认")
	// ErrOutsideSession 表示 K 线时间不落在任何交易时段（或不是交易日），无法识别是哪一根。
	ErrOutsideSession = errors.New("K线时间不在交易时段内")
)

// maxCalendarWalkDays 是沿交易日历前后查找的最大天数：足以跨过最长的长假（春节约 9 天）。
const maxCalendarWalkDays = 40

// maxCountedBars 是数缺失根数的上限，避免长时间中断时逐根遍历过久；超过时按上限计。
const maxCountedBars = 10000

// TradingDays 判断某天是否交易日；覆盖范围外返回 OutOfCoverage 与错误。marketcalendar.TradingCalendar 实现它。
type TradingDays interface {
	Status(marketcalendar.CivilDate) (marketcalendar.CoverageStatus, error)
}

// Bar 是一根 K 线：Open 是它在存储里的 data_time（开盘时间），Close 是收线时间。
type Bar struct {
	Open, Close time.Time
}

// MarketClock 按市场的交易日历切分 K 线、算出“此刻应已收线的最新一根”。
//
// 24 小时市场（加密货币）按 UTC 固定网格取整，周线按周一对齐，月线按自然月。交易所市场（A 股）只在交易日的交易时段内：
// 1 分钟线在每个时段内逐分钟切分（09:30～11:29、13:00～14:59 开盘）；日线、周线、月线每个交易日（周、月）一根，
// 在最后一个交易日的最后一个时段收盘时收线。非交易日、未开盘、午休时，应收的最新一根回溯到上一时段或上一交易日的最后一根。
type MarketClock struct {
	exchange bool
	location *time.Location
	days     TradingDays
	sessions []sessionSpan
}

// sessionSpan 是一个交易时段，用距本地零点的时长表示。
type sessionSpan struct {
	open, close time.Duration
}

// ContinuousClock 是全天交易的市场（加密货币）。
func ContinuousClock() MarketClock { return MarketClock{} }

// NewExchangeClock 按交易日历和交易时段（形如 "09:30-11:30"，按开盘先后排列）构造交易所市场的时钟。
func NewExchangeClock(days TradingDays, location *time.Location, sessions []string) (MarketClock, error) {
	if days == nil || location == nil {
		return MarketClock{}, ErrNoTradingCalendar
	}
	clock := MarketClock{exchange: true, location: location, days: days}
	for _, raw := range sessions {
		open, close, ok := strings.Cut(strings.TrimSpace(raw), "-")
		start, startErr := clockOfDay(open)
		end, endErr := clockOfDay(close)
		if !ok || startErr != nil || endErr != nil || end <= start {
			return MarketClock{}, fmt.Errorf("交易时段 %q 无效", raw)
		}
		if len(clock.sessions) > 0 && start < clock.sessions[len(clock.sessions)-1].close {
			return MarketClock{}, fmt.Errorf("交易时段 %q 与前一个时段重叠或顺序不对", raw)
		}
		clock.sessions = append(clock.sessions, sessionSpan{open: start, close: end})
	}
	if len(clock.sessions) == 0 {
		return MarketClock{}, fmt.Errorf("%w：没有交易时段", ErrNoTradingCalendar)
	}
	return clock, nil
}

// MarketClockFor 按采集任务清单声明的市场、日历、时区和交易时段选择时钟：加密货币全天交易，其余市场必须有内嵌的交易日历。
func MarketClockFor(marketID, calendarID, timezone string, sessions []string) (MarketClock, error) {
	if strings.TrimSpace(marketID) == "crypto" {
		return ContinuousClock(), nil
	}
	calendarID = strings.TrimSpace(calendarID)
	if calendarID == "" {
		return MarketClock{}, ErrNoTradingCalendar
	}
	calendar, err := marketcalendar.Load(calendarID)
	if err != nil {
		return MarketClock{}, fmt.Errorf("%w：%v", ErrNoTradingCalendar, err)
	}
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil {
		return MarketClock{}, fmt.Errorf("%w：时区 %q 无效", ErrNoTradingCalendar, timezone)
	}
	return NewExchangeClock(calendar, location, sessions)
}

func clockOfDay(value string) (time.Duration, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return time.Duration(parsed.Hour())*time.Hour + time.Duration(parsed.Minute())*time.Minute, nil
}

// BarAt 返回 data_time 为 dataTime 的那一根。
func (c MarketClock) BarAt(dataTime time.Time, freq frequency.Frequency) (Bar, error) {
	if !c.exchange {
		return continuousBar(dataTime, freq)
	}
	switch {
	case freq == frequency.Minute1:
		local := dataTime.In(c.location)
		day := localMidnight(local)
		if err := c.requireTradingDay(day); err != nil {
			return Bar{}, err
		}
		offset := local.Sub(day)
		for _, session := range c.sessions {
			if offset >= session.open && offset < session.close {
				open := local.Truncate(time.Minute)
				return Bar{Open: open.UTC(), Close: open.Add(time.Minute).UTC()}, nil
			}
		}
		return Bar{}, fmt.Errorf("%w：%s", ErrOutsideSession, local.Format("2006-01-02 15:04"))
	case isDailyOrLonger(freq):
		return c.periodBar(localMidnight(dataTime.In(c.location)), freq)
	default:
		return Bar{}, fmt.Errorf("%w：%s", ErrAlignmentUnconfirmed, freq)
	}
}

// LatestClosed 返回 now 时已收线的最新一根（应收的最新一根）。
func (c MarketClock) LatestClosed(now time.Time, freq frequency.Frequency) (Bar, error) {
	if !c.exchange {
		current, err := continuousBar(now, freq)
		if err != nil {
			return Bar{}, err
		}
		return Bar{Open: previousOpen(current.Open, freq), Close: current.Open}, nil
	}
	switch {
	case freq == frequency.Minute1:
		return c.latestClosedMinute(now)
	case isDailyOrLonger(freq):
		return c.latestClosedPeriod(now, freq)
	default:
		return Bar{}, fmt.Errorf("%w：%s", ErrAlignmentUnconfirmed, freq)
	}
}

// Next 返回 bar 之后的那一根。
func (c MarketClock) Next(bar Bar, freq frequency.Frequency) (Bar, error) {
	if !c.exchange {
		return continuousBar(bar.Close, freq)
	}
	switch {
	case freq == frequency.Minute1:
		return c.nextMinute(bar)
	case isDailyOrLonger(freq):
		day := localMidnight(bar.Close.In(c.location))
		next, err := c.tradingDayAfter(day)
		if err != nil {
			return Bar{}, err
		}
		return c.periodBar(next, freq)
	default:
		return Bar{}, fmt.Errorf("%w：%s", ErrAlignmentUnconfirmed, freq)
	}
}

// ClosedBarsAfter 数 latest 之后、收线时间不晚于 until 的根数（最多数到 maxCountedBars），并返回其中最早的一根。
func (c MarketClock) ClosedBarsAfter(latest Bar, until time.Time, freq frequency.Frequency) (int, Bar, error) {
	var first Bar
	count := 0
	cursor := latest
	for count < maxCountedBars {
		next, err := c.Next(cursor, freq)
		if err != nil {
			return 0, Bar{}, err
		}
		if next.Close.After(until) {
			break
		}
		if count == 0 {
			first = next
		}
		count++
		cursor = next
	}
	return count, first, nil
}

func isDailyOrLonger(freq frequency.Frequency) bool {
	return freq == frequency.Day1 || freq == frequency.Week1 || freq == frequency.Month1
}

// ---------- 24 小时市场 ----------

// mondayEpoch 是周线网格的起点：1970-01-05 是周一（Unix 零点是周四）。
var mondayEpoch = time.Date(1970, 1, 5, 0, 0, 0, 0, time.UTC)

func continuousBar(t time.Time, freq frequency.Frequency) (Bar, error) {
	t = t.UTC()
	switch freq {
	case frequency.Month1:
		open := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		return Bar{Open: open, Close: open.AddDate(0, 1, 0)}, nil
	case frequency.Week1:
		week := 7 * 24 * time.Hour
		elapsed := t.Sub(mondayEpoch)
		open := mondayEpoch.Add(elapsed - ((elapsed%week)+week)%week)
		return Bar{Open: open, Close: open.Add(week)}, nil
	}
	duration := freq.Duration()
	if duration <= 0 {
		return Bar{}, fmt.Errorf("不支持的周期 %q", freq)
	}
	// time.Truncate 以公元元年零点为基准取整，能整除一天的周期都对齐到 UTC 零点。
	open := t.Truncate(duration)
	return Bar{Open: open, Close: open.Add(duration)}, nil
}

func previousOpen(open time.Time, freq frequency.Frequency) time.Time {
	switch freq {
	case frequency.Month1:
		return open.AddDate(0, -1, 0)
	case frequency.Week1:
		return open.Add(-7 * 24 * time.Hour)
	default:
		return open.Add(-freq.Duration())
	}
}

// ---------- 交易所市场 ----------

func localMidnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func civilDate(day time.Time) (marketcalendar.CivilDate, error) {
	return marketcalendar.NewCivilDate(day.Year(), day.Month(), day.Day())
}

// isTradingDay 判断本地零点 day 是否交易日；覆盖范围外返回 ErrCalendarNotCovered。
func (c MarketClock) isTradingDay(day time.Time) (bool, error) {
	date, err := civilDate(day)
	if err != nil {
		return false, err
	}
	status, err := c.days.Status(date)
	if err != nil || status == marketcalendar.OutOfCoverage {
		return false, fmt.Errorf("%w：%s", ErrCalendarNotCovered, day.Format("2006-01-02"))
	}
	return status == marketcalendar.TradingDay, nil
}

func (c MarketClock) requireTradingDay(day time.Time) error {
	trading, err := c.isTradingDay(day)
	if err != nil {
		return err
	}
	if !trading {
		return fmt.Errorf("%w：%s 不是交易日", ErrOutsideSession, day.Format("2006-01-02"))
	}
	return nil
}

func (c MarketClock) tradingDayAfter(day time.Time) (time.Time, error) {
	for i := 1; i <= maxCalendarWalkDays; i++ {
		candidate := day.AddDate(0, 0, i)
		trading, err := c.isTradingDay(candidate)
		if err != nil {
			return time.Time{}, err
		}
		if trading {
			return candidate, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w：%s 之后 %d 天内没有交易日", ErrCalendarNotCovered, day.Format("2006-01-02"), maxCalendarWalkDays)
}

func (c MarketClock) latestClosedMinute(now time.Time) (Bar, error) {
	local := now.In(c.location)
	today := localMidnight(local)
	for i := 0; i <= maxCalendarWalkDays; i++ {
		day := today.AddDate(0, 0, -i)
		trading, err := c.isTradingDay(day)
		if err != nil {
			return Bar{}, err
		}
		if !trading {
			continue
		}
		for index := len(c.sessions) - 1; index >= 0; index-- {
			session := c.sessions[index]
			open, close := day.Add(session.open), day.Add(session.close)
			if !close.After(local) {
				return Bar{Open: close.Add(-time.Minute).UTC(), Close: close.UTC()}, nil
			}
			if !open.Add(time.Minute).After(local) {
				// 时段进行中：最近一个已过的整分钟就是最新一根的收线时刻。
				end := local.Truncate(time.Minute)
				return Bar{Open: end.Add(-time.Minute).UTC(), Close: end.UTC()}, nil
			}
		}
	}
	return Bar{}, fmt.Errorf("%w：%s 之前 %d 天内没有交易日", ErrCalendarNotCovered, today.Format("2006-01-02"), maxCalendarWalkDays)
}

func (c MarketClock) nextMinute(bar Bar) (Bar, error) {
	local := bar.Close.In(c.location)
	day := localMidnight(local)
	offset := local.Sub(day)
	for _, session := range c.sessions {
		if offset >= session.open && offset < session.close {
			return Bar{Open: local.UTC(), Close: local.Add(time.Minute).UTC()}, nil
		}
		if offset < session.open {
			// 午休或开盘前：下一根是这个时段的第一根。
			trading, err := c.isTradingDay(day)
			if err != nil {
				return Bar{}, err
			}
			if trading {
				open := day.Add(session.open)
				return Bar{Open: open.UTC(), Close: open.Add(time.Minute).UTC()}, nil
			}
			break
		}
	}
	next, err := c.tradingDayAfter(day)
	if err != nil {
		return Bar{}, err
	}
	open := next.Add(c.sessions[0].open)
	return Bar{Open: open.UTC(), Close: open.Add(time.Minute).UTC()}, nil
}

// periodKey 是交易日所属的周期：日线是日期本身，周线是 ISO 周，月线是年月。
func periodKey(day time.Time, freq frequency.Frequency) string {
	switch freq {
	case frequency.Week1:
		year, week := day.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	case frequency.Month1:
		return day.Format("2006-01")
	default:
		return day.Format("2006-01-02")
	}
}

// periodBar 返回 day 所属周期的那一根：开盘取周期内第一个交易日的本地零点（与日线及以上的 data_time 约定一致），
// 收线是周期内最后一个交易日的最后一个时段收盘。
func (c MarketClock) periodBar(day time.Time, freq frequency.Frequency) (Bar, error) {
	key := periodKey(day, freq)
	var first, last time.Time
	for i := -maxCalendarWalkDays; i <= maxCalendarWalkDays; i++ {
		candidate := day.AddDate(0, 0, i)
		if periodKey(candidate, freq) != key {
			if i > 0 {
				break
			}
			continue
		}
		trading, err := c.isTradingDay(candidate)
		if err != nil {
			return Bar{}, err
		}
		if trading {
			if first.IsZero() {
				first = candidate
			}
			last = candidate
		}
	}
	if first.IsZero() {
		return Bar{}, fmt.Errorf("%w：%s 所在周期没有交易日", ErrOutsideSession, day.Format("2006-01-02"))
	}
	return Bar{Open: first.UTC(), Close: last.Add(c.sessions[len(c.sessions)-1].close).UTC()}, nil
}

func (c MarketClock) latestClosedPeriod(now time.Time, freq frequency.Frequency) (Bar, error) {
	today := localMidnight(now.In(c.location))
	for i := 0; i <= maxCalendarWalkDays; i++ {
		day := today.AddDate(0, 0, -i)
		trading, err := c.isTradingDay(day)
		if err != nil {
			return Bar{}, err
		}
		if !trading {
			continue
		}
		bar, err := c.periodBar(day, freq)
		if err != nil {
			return Bar{}, err
		}
		if !bar.Close.After(now) {
			return bar, nil
		}
	}
	return Bar{}, fmt.Errorf("%w：%s 之前 %d 天内没有已收线的%s", ErrCalendarNotCovered, today.Format("2006-01-02"), maxCalendarWalkDays, freq)
}
