package input

import (
	"strconv"
	"strings"
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

// 预热区间：写了 start 从它开始；写了 initial 从 at 往前共 bars 期；都没写从最新已收盘的一根往前共 bars 期。
// 起点晚于最新已收盘的一根、或要推进的根数超过上限时拒绝，错误里写明需要多少根、上限多少。
func TestPoolWarmWindowStartsAtDriftStart(t *testing.T) {
	resolved := Resolved{Calendar: DefaultCalendar, Bar: "1h"}
	now := time.Date(2026, 3, 1, 10, 30, 0, 0, time.UTC)
	closedEnd := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC).Add(time.Nanosecond) // 最新已收盘的一根 bar_start 09:00

	start, end, err := PoolWarmWindow(resolved, driftPool(t, ""), 10, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), start)
	require.Equal(t, closedEnd, end)

	start, end, err = PoolWarmWindow(resolved, driftPool(t, "    start: \"2026-02-01T00:00:00Z\"\n"), 10, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), start)
	require.Equal(t, closedEnd, end)

	start, _, err = PoolWarmWindow(resolved, driftPool(t, "    initial: {at: \"2026-02-01T00:00:00Z\", ratios: {a: 0.5}}\n"), 10, now, time.Minute)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 1, 31, 15, 0, 0, 0, time.UTC), start, "at 是 10 期里的最后一期")

	_, _, err = PoolWarmWindow(resolved, driftPool(t, "    start: \"2026-03-01T10:00:00Z\"\n"), 10, now, time.Minute)
	require.ErrorContains(t, err, "晚于最新已收盘的一根")

	// 2023-06-01 到 2026-03-01 09:00 超过 20000 根。
	_, _, err = PoolWarmWindow(resolved, driftPool(t, "    start: \"2023-06-01T00:00:00Z\"\n"), 10, now, time.Minute)
	require.Error(t, err)
	hours := int(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC).Sub(time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC))/time.Hour) + 1
	require.True(t, strings.Contains(err.Error(), "需要推进 "+strconv.Itoa(hours)+" 根") && strings.Contains(err.Error(), "上限 20000 根"), err.Error())
}

// 启用时：drift 的起点不是 bar 起点就拒绝；起点早于 View 的实际覆盖（预热装不下）时拒绝启用，起点在覆盖之内时通过。
func TestDriftPoolEnableChecksStartAndCoverage(t *testing.T) {
	members := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}
	resolve := func(start time.Time) (PoolResolved, *fakeClient, error) {
		client := newDualClient()
		raw := strings.Replace(poolDSL, "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}",
			"allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, start: \""+start.Format(time.RFC3339)+"\"}", 1)
		pooled, err := ResolvePool(t.Context(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, raw), members)
		return pooled, client, err
	}
	if _, _, err := resolve(barStart.Add(-100*time.Hour + 30*time.Minute)); err == nil || !strings.Contains(err.Error(), "不是 1h 周期某根 bar 的起点") {
		t.Fatalf("起点不是 bar 起点应拒绝：%v", err)
	}
	now := barStart.Add(time.Hour + 30*time.Minute)
	pooled, client, err := resolve(barStart.Add(-100 * time.Hour))
	require.NoError(t, err)
	require.NoError(t, CheckPoolWarmCoverage(t.Context(), client, "space", pooled, now), "起点在覆盖之内")
	// View 只有 500 根历史：从 600 根之前开始预热装不下。
	pooled, client, err = resolve(barStart.Add(-600 * time.Hour))
	require.NoError(t, err)
	require.ErrorContains(t, CheckPoolWarmCoverage(t.Context(), client, "space", pooled, now), "不能启用")
}
