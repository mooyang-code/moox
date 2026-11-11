package input

import (
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
)

// 策略池的漂移起点与 periodic 锚点（设计 P3、P8）：DSL 里写的时刻都指某根 bar 的 bar_start（交易日期指该日第一根），
// 池不写 market，保存时分不出加密与 A 股，所以在启用时（这里）按池绑定的日历与周期换成 bar 序号，交给 pool.Config；
// 预热区间的起点也按它们计算。

// PoolClock 把池 DSL 里的 allocation.start、allocation.initial.at 与 allocation.sides.*.anchor 换成 bar 序号；
// 时刻不是这个日历与周期上某根 bar 的起点时返回错误（启用时据此拒绝）。都没写时返回零值。
func PoolClock(calendarID, frequency string, pool dsl.StrategyPool) (engine.PoolClock, error) {
	var clock engine.PoolClock
	if text := pool.Allocation.DriftStart(); text != "" {
		bar, err := anchorBoundaries(calendarID, frequency, text)
		if err != nil {
			return engine.PoolClock{}, fmt.Errorf("%s %w", driftStartField(pool), err)
		}
		clock.Start, clock.HasStart = bar.BarIndex, true
	}
	for _, side := range []string{dsl.SideLong, dsl.SideShort} {
		text := pool.Side(side).Anchor
		if text == "" {
			continue
		}
		bar, err := anchorBoundaries(calendarID, frequency, text)
		if err != nil {
			return engine.PoolClock{}, fmt.Errorf("strategy_pool.allocation.sides.%s.anchor %w", side, err)
		}
		if clock.Anchors == nil {
			clock.Anchors = make(map[string]int64, 2)
		}
		clock.Anchors[side] = bar.BarIndex
	}
	return clock, nil
}

func driftStartField(pool dsl.StrategyPool) string {
	if pool.Allocation.Initial != nil {
		return "strategy_pool.allocation.initial.at"
	}
	return "strategy_pool.allocation.start"
}

// anchorBoundaries 返回 DSL 时刻所在那根 bar 的周期边界：交易日期取该日第一根；RFC3339 时刻必须恰好是某根的 bar_start。
func anchorBoundaries(calendarID, frequency, text string) (PeriodBoundaries, error) {
	axis, err := dayAxisFor(calendarID, frequency)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	index, err := anchorBar(calendarID, frequency, axis, text)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	anchor, err := dsl.ParseAnchor(text)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	at := anchor.At
	if !anchor.Date.IsZero() {
		// A 股日内的 bar_start 是真实的开盘时刻：从该日的上海零点起找第一根。A 股日线的 bar_start 是日期键，
		// 与 crypto 一样就是该日期的 UTC 零点。
		location := time.UTC
		if stockIntraday(calendarID, frequency) {
			if _, location, err = stockCalendar(); err != nil {
				return PeriodBoundaries{}, err
			}
		}
		midnight := time.Date(anchor.Date.Year(), anchor.Date.Month(), anchor.Date.Day(), 0, 0, 0, 0, location)
		if at, err = CeilStorageStart(calendarID, frequency, midnight); err != nil {
			return PeriodBoundaries{}, err
		}
	}
	bar, err := FromStorageStart(calendarID, frequency, at)
	if err != nil {
		return PeriodBoundaries{}, err
	}
	if bar.BarIndex != index {
		return PeriodBoundaries{}, fmt.Errorf("%s 换算出的 bar 序号不一致（%d 与 %d）", text, bar.BarIndex, index)
	}
	return bar, nil
}
