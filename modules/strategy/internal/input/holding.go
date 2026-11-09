package input

import (
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// 换仓时钟（holding.anchor 与 holding.calendar）：引擎是纯函数，交易日历的换算都在输入层完成，实时、回放、预热共用这里。
//
// 交易日序号：加密按 UTC 自然日（1970-01-01 为 0，每天都是交易日），A 股按内嵌交易日历（首个交易日为 0）。
// bar 序号与 PeriodBoundaries.BarIndex 一致：加密是 bar_end ÷ 周期，A 股日线是交易日序号，A 股日内是“交易日序号 × 每日根数 + 日内序号”。

// maxCalendarScan 是日历写法向前找最近一个建仓日时最多回看的交易日数：超过一年仍找不到（例如每周第 7 个交易日在 A 股不存在），
// 视为还没有过应建仓 bar。
const maxCalendarScan = 400

// dayAxis 是一种日历与周期组合上的交易日轴。
type dayAxis interface {
	// dayOf 返回 bar 所在的交易日与序号。
	dayOf(bar PeriodBoundaries) (marketcalendar.CivilDate, int64, error)
	// dayIndex 返回交易日的序号；不是交易日时报错。
	dayIndex(date marketcalendar.CivilDate) (int64, error)
	// previous 返回上一个交易日；没有时返回 false。
	previous(date marketcalendar.CivilDate, index int64) (marketcalendar.CivilDate, int64, bool, error)
	// position 返回交易日在自然周或自然月里是第几个交易日（从 1 起）与该周期的交易日总数。
	position(date marketcalendar.CivilDate, period marketcalendar.Period) (int, int, error)
	// firstBar、lastBar 返回交易日第一根、最后一根 bar 的序号。
	firstBar(index int64) int64
	lastBar(index int64) int64
}

func dayAxisFor(calendarID, frequency string) (dayAxis, error) {
	clock, err := clockFor(calendarID, frequency)
	if err != nil {
		return nil, err
	}
	switch typed := clock.(type) {
	case fixedClock:
		if typed.duration <= 0 || (24*time.Hour)%typed.duration != 0 {
			return nil, fmt.Errorf("holding 的锚点与日历换仓需要周期整除一天，%s 不满足", frequency)
		}
		return cryptoAxis{perDay: int64(24 * time.Hour / typed.duration)}, nil
	case stockDailyClock:
		return stockAxis{calendar: typed.calendar, perDay: 1}, nil
	case *stockSessionClock:
		return stockAxis{calendar: typed.calendar, perDay: int64(typed.perDay)}, nil
	}
	return nil, fmt.Errorf("%w：日历 %q 不支持换仓时钟", ErrUnsupportedCalendar, calendarID)
}

// cryptoAxis 是加密 24x7：每个 UTC 自然日都是交易日，周从周一开始，月从 1 日开始。
type cryptoAxis struct{ perDay int64 }

var unixEpochDay = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

func civilOf(at time.Time) (marketcalendar.CivilDate, error) {
	return marketcalendar.NewCivilDate(at.Year(), at.Month(), at.Day())
}

func (a cryptoAxis) dayOf(bar PeriodBoundaries) (marketcalendar.CivilDate, int64, error) {
	start := bar.StorageStart.UTC()
	date, err := civilOf(start)
	if err != nil {
		return marketcalendar.CivilDate{}, 0, err
	}
	index, err := a.dayIndex(date)
	return date, index, err
}

func (cryptoAxis) dayIndex(date marketcalendar.CivilDate) (int64, error) {
	at := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	return int64(at.Sub(unixEpochDay) / (24 * time.Hour)), nil
}

func (cryptoAxis) previous(date marketcalendar.CivilDate, index int64) (marketcalendar.CivilDate, int64, bool, error) {
	at := time.Date(date.Year(), date.Month(), date.Day()-1, 0, 0, 0, 0, time.UTC)
	previous, err := civilOf(at)
	if err != nil {
		return marketcalendar.CivilDate{}, 0, false, nil
	}
	return previous, index - 1, true, nil
}

func (cryptoAxis) position(date marketcalendar.CivilDate, period marketcalendar.Period) (int, int, error) {
	at := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	if period == marketcalendar.PeriodWeek {
		return (int(at.Weekday())+6)%7 + 1, 7, nil
	}
	return date.Day(), time.Date(date.Year(), date.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day(), nil
}

// 加密 bar 序号是 bar_end ÷ 周期：当日第一根的 bar_end 是 0 点加一个周期，最后一根的 bar_end 是次日 0 点。
func (a cryptoAxis) firstBar(index int64) int64 { return index*a.perDay + 1 }
func (a cryptoAxis) lastBar(index int64) int64  { return (index + 1) * a.perDay }

// stockAxis 是 A 股：按内嵌交易日历，日线每日一根，日内每日 perDay 根。
type stockAxis struct {
	calendar marketcalendar.TradingCalendar
	perDay   int64
}

func (a stockAxis) dayOf(bar PeriodBoundaries) (marketcalendar.CivilDate, int64, error) {
	index := bar.BarIndex / a.perDay
	date, err := a.calendar.TradingDayAt(int(index))
	if err != nil {
		return marketcalendar.CivilDate{}, 0, translateCalendarError(err)
	}
	return date, index, nil
}

func (a stockAxis) dayIndex(date marketcalendar.CivilDate) (int64, error) {
	index, err := a.calendar.TradingDayIndex(date)
	if err != nil {
		return 0, stockDayError(date, err)
	}
	return int64(index), nil
}

func (a stockAxis) previous(date marketcalendar.CivilDate, index int64) (marketcalendar.CivilDate, int64, bool, error) {
	previous, err := a.calendar.PrevTradingDay(date)
	if errors.Is(err, marketcalendar.ErrNoPreviousTradingDay) {
		return marketcalendar.CivilDate{}, 0, false, nil
	}
	if err != nil {
		return marketcalendar.CivilDate{}, 0, false, stockDayError(date, err)
	}
	return previous, index - 1, true, nil
}

func (a stockAxis) position(date marketcalendar.CivilDate, period marketcalendar.Period) (int, int, error) {
	position, total, err := a.calendar.PeriodPosition(date, period)
	if err != nil {
		return 0, 0, stockDayError(date, err)
	}
	return position, total, nil
}

func (a stockAxis) firstBar(index int64) int64 { return index * a.perDay }
func (a stockAxis) lastBar(index int64) int64  { return index*a.perDay + a.perDay - 1 }

// anchorBar 把 holding.anchor 换成 bar 序号：交易日期取该日第一根；RFC3339 时刻必须恰好是某根的 bar_start。
func anchorBar(calendarID, frequency string, axis dayAxis, text string) (int64, error) {
	anchor, err := dsl.ParseAnchor(text)
	if err != nil {
		return 0, err
	}
	if !anchor.Date.IsZero() {
		index, err := axis.dayIndex(anchor.Date)
		if err != nil {
			return 0, err
		}
		return axis.firstBar(index), nil
	}
	bar, err := FromStorageStart(calendarID, frequency, anchor.At)
	if err == nil && !bar.StorageStart.Equal(anchor.At) {
		err = errors.New("不是 bar 起点")
	}
	// 加密的定长周期按纪元对齐，换算本身不检查对齐，这里补上。
	if crypto, ok := axis.(cryptoAxis); ok && err == nil && anchor.At.UnixNano()%(int64(24*time.Hour)/crypto.perDay) != 0 {
		err = errors.New("不是 bar 起点")
	}
	if err != nil {
		return 0, fmt.Errorf("%s 不是 %s 周期某根 bar 的起点（bar_start）：%v", text, frequency, err)
	}
	return bar.BarIndex, nil
}

// anchorDay 返回 trading_days 写法的周期起点（交易日序号）：没写 anchor 时加密是 1970-01-01、A 股是内嵌日历的首个交易日，都是 0。
func anchorDay(calendarID, frequency string, axis dayAxis, text string) (int64, error) {
	if text == "" {
		return 0, nil
	}
	anchor, err := dsl.ParseAnchor(text)
	if err != nil {
		return 0, err
	}
	if !anchor.Date.IsZero() {
		return axis.dayIndex(anchor.Date)
	}
	bar, err := anchorBar(calendarID, frequency, axis, text)
	if err != nil {
		return 0, err
	}
	boundaries, err := FromStorageStart(calendarID, frequency, anchor.At)
	if err != nil {
		return 0, err
	}
	boundaries.BarIndex = bar
	_, index, err := axis.dayOf(boundaries)
	return index, err
}

// CheckHoldingAnchors 在启用时检查各规则的 holding.anchor 能换成这个日历与周期上的 bar 序号（时刻不是 bar 起点时拒绝），
// 以及日历写法所需的交易日轴可用。
func CheckHoldingAnchors(calendarID, frequency string, strategy dsl.Strategy) error {
	for _, rule := range strategy.Rules {
		holding := rule.Holding
		if holding == nil || (holding.Anchor == "" && !holding.IsCalendar()) {
			continue
		}
		axis, err := dayAxisFor(calendarID, frequency)
		if err != nil {
			return fmt.Errorf("规则 %s：%w", rule.ID, err)
		}
		if holding.IsCalendar() {
			_, err = anchorDay(calendarID, frequency, axis, holding.Anchor)
		} else {
			_, err = anchorBar(calendarID, frequency, axis, holding.Anchor)
		}
		if err != nil {
			return fmt.Errorf("规则 %s 的 holding.anchor %w", rule.ID, err)
		}
	}
	return nil
}

// HoldingClocks 计算写了 holding.anchor 或 holding.calendar 的规则本根的换仓时钟（见 engine.HoldingClock）；其余规则不出现。
func HoldingClocks(calendarID, frequency string, strategy dsl.Strategy, bar PeriodBoundaries) (map[string]engine.HoldingClock, error) {
	var clocks map[string]engine.HoldingClock
	for _, rule := range strategy.Rules {
		holding := rule.Holding
		if holding == nil || (holding.Anchor == "" && !holding.IsCalendar()) {
			continue
		}
		axis, err := dayAxisFor(calendarID, frequency)
		if err != nil {
			return nil, fmt.Errorf("规则 %s：%w", rule.ID, err)
		}
		var clock engine.HoldingClock
		if holding.IsCalendar() {
			clock.Scheduled, err = scheduledBars(calendarID, frequency, axis, holding, bar)
		} else {
			clock.Anchor, err = anchorBar(calendarID, frequency, axis, holding.Anchor)
		}
		if err != nil {
			return nil, fmt.Errorf("规则 %s 的 holding：%w", rule.ID, err)
		}
		if clocks == nil {
			clocks = make(map[string]engine.HoldingClock)
		}
		clocks[rule.ID] = clock
	}
	return clocks, nil
}

// scheduledBars 返回日历写法每个批次最近一个应建仓 bar 的序号（不晚于本根）：建仓日是满足 days 的交易日，日内帧在该日的
// 第一根或最后一根（at）。当周（当月）交易日少于指定序号时这一期没有建仓日，继续往前找。
func scheduledBars(calendarID, frequency string, axis dayAxis, holding *dsl.Holding, bar PeriodBoundaries) (map[int]int64, error) {
	today, todayIndex, err := axis.dayOf(bar)
	if err != nil {
		return nil, err
	}
	establish := axis.lastBar
	if !holding.AtLast() {
		establish = axis.firstBar
	}
	scheduled := make(map[int]int64, len(holding.Days))
	if holding.Calendar == dsl.CalendarTradingDays {
		anchor, err := anchorDay(calendarID, frequency, axis, holding.Anchor)
		if err != nil {
			return nil, err
		}
		every := int64(holding.Every)
		for _, day := range holding.Days {
			index := todayIndex - floorModInt(todayIndex-anchor-int64(day-1), every)
			if establish(index) > bar.BarIndex {
				index -= every
			}
			if index >= 0 || calendarID == DefaultCalendar {
				scheduled[day] = establish(index)
			}
		}
		return scheduled, nil
	}
	period := marketcalendar.PeriodWeek
	if holding.Calendar == dsl.CalendarMonth {
		period = marketcalendar.PeriodMonth
	}
	pending := make(map[int]struct{}, len(holding.Days))
	for _, day := range holding.Days {
		pending[day] = struct{}{}
	}
	date, index := today, todayIndex
	for step := 0; step < maxCalendarScan && len(pending) > 0; step++ {
		position, total, err := axis.position(date, period)
		if err != nil {
			return nil, err
		}
		for day := range pending {
			matched := position == day || (day < 0 && position == total+day+1)
			if matched && establish(index) <= bar.BarIndex {
				scheduled[day] = establish(index)
				delete(pending, day)
			}
		}
		var ok bool
		if date, index, ok, err = axis.previous(date, index); err != nil {
			return nil, err
		} else if !ok {
			break
		}
	}
	return scheduled, nil
}

func floorModInt(value, m int64) int64 { return (value%m + m) % m }
