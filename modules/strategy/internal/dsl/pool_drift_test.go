package dsl

import (
	"strings"
	"testing"
)

func TestParsePoolDrift(t *testing.T) {
	t.Parallel()
	strategy, err := parsePool(t, `  allocation:
    method: drift
    ratios: {a: 0.6, b: 0.4}
    start: "2026-01-17T07:00:00Z"
    sides:
      long: {rebalance: every_bar}
      short:
        rebalance: both
        period_bars: 2160
        anchor: "2026-01-17T07:00:00Z"
        threshold: 0.6
        spread_bars: 24
        drift_by: nav
    switch: [{when: {empty: a}, move: {from: a, to: [b]}}]
  freeze: {watch: short, window_bars: 3, threshold: 0.1, bars: 4}
portfolio: {leverage: 2}
`)
	if err != nil {
		t.Fatal(err)
	}
	pool := strategy.Pool
	if pool.Allocation.Method != AllocationDrift || pool.Allocation.Start != "2026-01-17T07:00:00Z" || pool.Allocation.DriftStart() != pool.Allocation.Start {
		t.Fatalf("allocation 解析不符：%+v", pool.Allocation)
	}
	short := pool.Side(SideShort)
	if short.Rebalance != RebalanceBoth || short.PeriodBars != 2160 || short.Threshold != 0.6 || short.SpreadBars != 24 || short.DriftBy != DriftByNAV || !short.Periodic() || !short.ByThreshold() {
		t.Fatalf("short 侧解析不符：%+v", short)
	}
	if long := pool.Side(SideLong); long.Rebalance != RebalanceEveryBar || long.SpreadBars != 1 || long.DriftBy != DriftByNotional {
		t.Fatalf("long 侧的默认值不符：%+v", long)
	}
	if freeze := pool.Freeze; freeze == nil || freeze.WatchSide != SideShort || freeze.WindowBars != 3 || freeze.Threshold != 0.1 || freeze.Bars != 4 {
		t.Fatalf("freeze 解析不符：%+v", freeze)
	}
	if pool.MaxWindow() != 3 || !pool.NeedsSingleSide() {
		t.Fatalf("冻结窗口要计入最长窗口，drift 要求成员单方向：%d %v", pool.MaxWindow(), pool.NeedsSingleSide())
	}
	// 没写 sides 的一侧不再平衡；initial 与成员列表形式的 watch。
	initial, err := parsePool(t, "  allocation:\n    method: drift\n    ratios: {a: 0.5, b: 0.5}\n    initial: {at: \"2026-01-01\", ratios: {a: 0.7, b: 0.3}}\n  freeze: {watch: [a], window_bars: 1, threshold: 0.2, bars: 1}\n")
	if err != nil {
		t.Fatal(err)
	}
	allocation := initial.Pool.Allocation
	if allocation.DriftStart() != "2026-01-01" || allocation.StartRatios()["a"].String() != "0.7" || allocation.Ratios["a"].String() != "0.5" {
		t.Fatalf("initial 解析不符：%+v", allocation)
	}
	if side := initial.Pool.Side(SideShort); side.Rebalance != RebalanceNone || side.SpreadBars != 1 {
		t.Fatalf("没写的一侧默认不再平衡：%+v", side)
	}
	if watch := initial.Pool.Freeze.WatchMembers; len(watch) != 1 || watch[0] != "a" {
		t.Fatalf("watch 成员列表：%v", watch)
	}
}

func TestParsePoolDriftRejectsIllegal(t *testing.T) {
	t.Parallel()
	const drift = "  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}}\n"
	side := func(body string) string {
		return "  allocation:\n    method: drift\n    ratios: {a: 0.5, b: 0.5}\n    sides: {long: " + body + "}\n"
	}
	cases := map[string]struct{ body, contains string }{
		// 互斥
		"drift 写 timing":          {drift + "  timing: {window: 5}\n", "不能写 timing"},
		"drift 写 step":            {drift + "  step: 0.1\n", "不能写 step"},
		"drift 写 rotation":        {"  allocation: {method: drift, ratios: {a: 1, b: 0}, rotation: {top: 1, factors: [{name: return, window: 5}]}}\n", "不能写 rotation"},
		"drift 缺 ratios":          {"  allocation: {method: drift}\n", "需要写 ratios"},
		"drift 缺成员比例":             {"  allocation: {method: drift, ratios: {a: 1}}\n", "缺少成员 b"},
		"fixed 写 sides":           {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, sides: {long: {rebalance: every_bar}}}\n", "sides 只能与 method: drift"},
		"fixed 写 start":           {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, start: \"2026-01-01\"}\n", "只能与 method: drift"},
		"rotation 写 initial":      {"  allocation: {method: rotation, rotation: {top: 1, factors: [{name: return, window: 5}]}, initial: {at: \"2026-01-01\", ratios: {a: 1, b: 0}}}\n", "只能与 method: drift"},
		"fixed 写 freeze":          {"  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n  freeze: {watch: short, window_bars: 1, threshold: 0.1, bars: 1}\n", "freeze 只能与"},
		"start 与 initial 同写":      {"  allocation:\n    method: drift\n    ratios: {a: 0.5, b: 0.5}\n    start: \"2026-01-01\"\n    initial: {at: \"2026-01-01\", ratios: {a: 1, b: 0}}\n", "只能写一个"},
		"period_bars 配 every_bar": {side("{rebalance: every_bar, period_bars: 5}"), "period_bars、anchor 只能与"},
		"anchor 配 threshold":      {side("{rebalance: threshold, threshold: 0.1, anchor: \"2026-01-01\"}"), "period_bars、anchor 只能与"},
		"threshold 配 periodic":    {side("{rebalance: periodic, period_bars: 5, threshold: 0.1}"), "threshold 只能与"},
		"spread_bars 配 none":      {side("{rebalance: none, spread_bars: 3}"), "spread_bars 只能与"},
		"没写 rebalance 写 spread":   {side("{spread_bars: 3}"), "spread_bars 只能与"},
		// 非法值
		"未知 method":         {"  allocation: {method: drifting, ratios: {a: 1, b: 0}}\n", "只能是 fixed、rotation 或 drift"},
		"sides 未知一侧":        {"  allocation:\n    method: drift\n    ratios: {a: 0.5, b: 0.5}\n    sides: {both: {rebalance: none}}\n", "只能写 long 与 short"},
		"rebalance 非法":      {side("{rebalance: daily}"), "rebalance 只能是"},
		"drift_by 非法":       {side("{drift_by: value}"), "drift_by 只能是"},
		"periodic 缺 period": {side("{rebalance: periodic}"), "period_bars 必须在 1 到"},
		"period_bars 超限":    {side("{rebalance: periodic, period_bars: 20000}"), "period_bars 必须在 1 到"},
		"anchor 写法非法":       {side("{rebalance: periodic, period_bars: 5, anchor: tomorrow}"), "anchor 必须是 RFC3339"},
		"threshold 缺值":      {side("{rebalance: threshold}"), "threshold 必须大于 0"},
		"threshold 为负":      {side("{rebalance: both, period_bars: 5, threshold: -0.1}"), "threshold 必须大于 0"},
		"spread_bars 为 0":   {side("{rebalance: every_bar, spread_bars: 0}"), "spread_bars 必须在 1 到"},
		"start 写法非法":        {"  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, start: soon}\n", "allocation.start 必须是 RFC3339"},
		"initial 缺 ratios":  {"  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, initial: {at: \"2026-01-01\"}}\n", "同时写 at"},
		"initial 缺 at":      {"  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, initial: {ratios: {a: 1, b: 0}}}\n", "同时写 at"},
		"initial.at 非法":     {"  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, initial: {at: x, ratios: {a: 1, b: 0}}}\n", "initial.at 必须是"},
		"initial 缺成员比例":     {"  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, initial: {at: \"2026-01-01\", ratios: {a: 1}}}\n", "allocation.initial.ratios 缺少成员 b"},
		"initial 比例为负":      {"  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, initial: {at: \"2026-01-01\", ratios: {a: -1, b: 0}}}\n", "allocation.initial.ratios.a 必须在 0 到"},
		"freeze 缺 watch":    {drift + "  freeze: {window_bars: 1, threshold: 0.1, bars: 1}\n", "freeze.watch 必须写"},
		"freeze watch 非法":   {drift + "  freeze: {watch: all, window_bars: 1, threshold: 0.1, bars: 1}\n", "只能是 long、short 或成员 id 列表"},
		"freeze watch 不是成员": {drift + "  freeze: {watch: [c], window_bars: 1, threshold: 0.1, bars: 1}\n", "不是池的成员"},
		"freeze watch 重复":   {drift + "  freeze: {watch: [a, a], window_bars: 1, threshold: 0.1, bars: 1}\n", "重复"},
		"freeze 窗口为 0":      {drift + "  freeze: {watch: short, window_bars: 0, threshold: 0.1, bars: 1}\n", "window_bars 必须在 1 到"},
		"freeze 阈值为 0":      {drift + "  freeze: {watch: short, window_bars: 1, threshold: 0, bars: 1}\n", "freeze.threshold 必须大于 0"},
		"freeze bars 为 0":   {drift + "  freeze: {watch: short, window_bars: 1, threshold: 0.1, bars: 0}\n", "freeze.bars 必须在 1 到"},
		"freeze 未知字段":       {drift + "  freeze: {watch: short, window_bars: 1, threshold: 0.1, bars: 1, side: long}\n", "未知字段"},
	}
	for name, tc := range cases {
		if _, err := parsePool(t, tc.body); err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Fatalf("%s：期望包含 %q，实际 %v", name, tc.contains, err)
		}
	}
}

// drift 要求成员单方向；freeze.watch 写的一侧要有成员；switch 可以与 drift 一起写（同方向）。
func TestPoolDriftMemberSides(t *testing.T) {
	t.Parallel()
	long := sideMember(t, "market: {select: swap}\n", SideLong)
	short := sideMember(t, "market: {select: swap}\n", SideShort)
	mixed, err := Parse([]byte("name: m\nbar: 1h\nmarket: {select: swap}\nrules:\n  - {id: l, type: rank, score: close, select: {top: 1}, weight: 0.5}\n  - {id: s, type: rank, score: close, select: {top: 1}, weight: 0.5, side: short}\n"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := parsePool(t, "  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}, switch: [{when: {empty: b}, move: {from: b, to: [a]}}]}\n  freeze: {watch: short, window_bars: 1, threshold: 0.1, bars: 1}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatalf("drift 可以写 switch：%v", err)
	}
	if _, err := ValidatePoolMembers(pool, map[string]Strategy{"a": short, "b": short}); err != nil {
		t.Fatalf("两个空头成员：%v", err)
	}
	if _, err := ValidatePoolMembers(pool, map[string]Strategy{"a": short, "b": mixed}); err == nil || !strings.Contains(err.Error(), "drift") || !strings.Contains(err.Error(), "单方向") {
		t.Fatalf("混合方向的成员应拒绝：%v", err)
	}
	longs, err := parsePool(t, "  allocation: {method: drift, ratios: {a: 0.5, b: 0.5}}\n  freeze: {watch: short, window_bars: 1, threshold: 0.1, bars: 1}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(longs, map[string]Strategy{"a": long, "b": long}); err == nil || !strings.Contains(err.Error(), "没有空头成员") {
		t.Fatalf("watch 的一侧没有成员应拒绝：%v", err)
	}
}
