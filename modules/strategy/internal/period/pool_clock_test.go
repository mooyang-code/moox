package period

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// driftPool 返回一个 drift 池的定义，allocation 是 method、ratios 之后追加的写法。
func driftPool(t *testing.T, allocation string) dsl.StrategyPool {
	t.Helper()
	raw := "name: pool\nbar: 1h\nstrategy_pool:\n  members:\n    - {id: a, strategy: strategy_a}\n  allocation:\n    method: drift\n    ratios: {a: 1}\n" + allocation
	return *parseStrategy(t, raw).Pool
}

// 漂移起点与 periodic 锚点换成 bar 序号：RFC3339 时刻必须是 bar 起点，交易日期取该日第一根；A 股日内取当日开盘那一根。
func TestPoolClockConvertsTimes(t *testing.T) {
	at := time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)
	bar, err := FromStorageStart(DefaultCalendar, "1h", at)
	require.NoError(t, err)
	clock, err := PoolClock(DefaultCalendar, "1h", driftPool(t, "    start: \"2026-01-01T02:00:00Z\"\n    sides: {short: {rebalance: periodic, period_bars: 24, anchor: \"2026-01-01\"}}\n"))
	require.NoError(t, err)
	midnight, err := FromStorageStart(DefaultCalendar, "1h", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.True(t, clock.HasStart)
	require.Equal(t, bar.BarIndex, clock.Start)
	require.Equal(t, map[string]int64{dsl.SideShort: midnight.BarIndex}, clock.Anchors)

	clock, err = PoolClock(DefaultCalendar, "1h", driftPool(t, "    initial: {at: \"2026-01-01T02:00:00Z\", ratios: {a: 0.5}}\n"))
	require.NoError(t, err)
	require.Equal(t, bar.BarIndex, clock.Start)

	none, err := PoolClock(DefaultCalendar, "1h", driftPool(t, ""))
	require.NoError(t, err)
	require.False(t, none.HasStart)
	require.Nil(t, none.Anchors)

	_, err = PoolClock(DefaultCalendar, "1h", driftPool(t, "    start: \"2026-01-01T02:30:00Z\"\n"))
	require.ErrorContains(t, err, "strategy_pool.allocation.start")
	require.ErrorContains(t, err, "不是 1h 周期某根 bar 的起点")
	_, err = PoolClock(DefaultCalendar, "4h", driftPool(t, "    sides: {long: {rebalance: periodic, period_bars: 6, anchor: \"2026-01-01T02:00:00Z\"}}\n"))
	require.ErrorContains(t, err, "sides.long.anchor")

	// A 股日线：交易日期就是这一天；日内：这一天的第一根（09:30 开盘）。非交易日拒绝。
	daily, err := PoolClock("cn_stock", "1d", driftPool(t, "    start: \"2026-01-05\"\n"))
	require.NoError(t, err)
	day, err := FromStorageStart("cn_stock", "1d", stockDay(t, 2026, 1, 5))
	require.NoError(t, err)
	require.Equal(t, day.BarIndex, daily.Start)
	intraday, err := PoolClock("cn_stock", "1h", driftPool(t, "    start: \"2026-01-05\"\n"))
	require.NoError(t, err)
	open, err := FromStorageStart("cn_stock", "1h", time.Date(2026, 1, 5, 9, 30, 0, 0, shanghai(t)))
	require.NoError(t, err)
	require.Equal(t, open.BarIndex, intraday.Start)
	_, err = PoolClock("cn_stock", "1d", driftPool(t, "    start: \"2026-01-03\"\n"))
	require.ErrorContains(t, err, "不是 A 股交易日")
}
