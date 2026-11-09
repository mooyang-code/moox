package dsl

import (
	"strings"
	"testing"
)

func sideMember(t *testing.T, extra, side string) Strategy {
	t.Helper()
	strategy, err := Parse([]byte("name: m\nbar: 1h\n" + extra + "rules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1, side: " + side + "}\n"))
	if err != nil {
		t.Fatal(err)
	}
	return strategy
}

func TestParsePoolConstraints(t *testing.T) {
	t.Parallel()
	strategy, err := parsePool(t, `  allocation:
    method: fixed
    ratios: {a: 0.6, b: 0.4}
    switch:
      - when: {empty: a}
        move: {from: a, to: [b]}
  exposure:
    bands:
      - {min: -0.1, max: 0.3, adjust: auto, pivot: 1.0}
      - {when_gross_below: 1.0, min: 0, max: 0, adjust: raise_smaller}
  gross: {min: 1.0, max: 1.8}
  fees: {equity_bps: 4, funding_column: funding_rate}
portfolio:
  leverage: 2
  limits: {long_max: 0.05, short_max: 0.03, overflow: {to: b}}
`)
	if err != nil {
		t.Fatal(err)
	}
	pool := strategy.Pool
	if len(pool.Allocation.Switch) != 1 || pool.Allocation.Switch[0].WhenEmpty != "a" || pool.Allocation.Switch[0].To[0] != "b" {
		t.Fatalf("switch 解析不符：%+v", pool.Allocation.Switch)
	}
	bands := pool.Exposure.Bands
	if len(bands) != 2 || bands[0].Adjust != AdjustAuto || bands[0].Pivot != 1 || !bands[1].HasBelow || bands[1].WhenGrossBelow != 1 {
		t.Fatalf("exposure 解析不符：%+v", bands)
	}
	if !pool.Gross.HasMin || pool.Gross.Max != 1.8 || pool.Fees.EquityBps != 4 || pool.Fees.FundingColumn != "funding_rate" || pool.Fees.SpotBps != DefaultPoolSpotFeeBps {
		t.Fatalf("gross 或 fees 解析不符：%+v %+v", pool.Gross, pool.Fees)
	}
	limits := strategy.Portfolio.Limits
	if limits == nil || limits.LongMax.String() != "0.05" || limits.ShortMax.String() != "0.03" || limits.OverflowTo != "b" {
		t.Fatalf("limits 解析不符：%+v", limits)
	}
	plain, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n")
	if err != nil {
		t.Fatal(err)
	}
	if plain.Pool.Fees.EquityBps != DefaultPoolEquityFeeBps || plain.Pool.Fees.FundingColumn != "" || plain.Pool.NeedsSingleSide() || plain.Portfolio.Limits != nil {
		t.Fatalf("默认值不符：%+v", plain.Pool)
	}
}

func TestParsePoolConstraintsRejectsIllegal(t *testing.T) {
	t.Parallel()
	const fixed = "  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n"
	cases := map[string]struct{ body, contains string }{
		"rotation 写 switch":   {"  allocation: {method: rotation, rotation: {top: 1, factors: [{name: return, window: 5}]}, switch: [{when: {empty: a}, move: {from: a, to: [b]}}]}\n", "只能与 method: fixed"},
		"switch 成员不存在":        {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, switch: [{when: {empty: c}, move: {from: a, to: [b]}}]}\n", "when.empty"},
		"switch from 不存在":     {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, switch: [{when: {empty: a}, move: {from: c, to: [b]}}]}\n", "move.from"},
		"switch to 含 from":    {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, switch: [{when: {empty: a}, move: {from: a, to: [a]}}]}\n", "不能包含 from"},
		"switch 缺 move":       {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, switch: [{when: {empty: a}}]}\n", "需要写 move"},
		"switch 未知条件":         {"  allocation: {method: fixed, ratios: {a: 1, b: 0}, switch: [{when: {full: a}, move: {from: a, to: [b]}}]}\n", "只有 empty"},
		"exposure 没有段":        {fixed + "  exposure: {bands: []}\n", "至少需要一段"},
		"exposure min 大于 max": {fixed + "  exposure: {bands: [{min: 0.5, max: 0.1, adjust: reduce_larger}]}\n", "min 不能大于 max"},
		"exposure 缺 max":      {fixed + "  exposure: {bands: [{min: 0, adjust: reduce_larger}]}\n", "同时写 min 与 max"},
		"exposure adjust 非法":  {fixed + "  exposure: {bands: [{min: 0, max: 0, adjust: shrink}]}\n", "adjust 只能是"},
		"auto 缺 pivot":        {fixed + "  exposure: {bands: [{min: 0, max: 0, adjust: auto}]}\n", "需要写 pivot"},
		"pivot 配非 auto":       {fixed + "  exposure: {bands: [{min: 0, max: 0, adjust: reduce_larger, pivot: 1}]}\n", "pivot 只能与"},
		"gross 都没写":           {fixed + "  gross: {}\n", "至少写 min、max"},
		"gross min 大于 max":    {fixed + "  gross: {min: 1, max: 0.5}\n", "min 不能大于 max"},
		"gross max 超杠杆":       {fixed + "  gross: {max: 1.5}\n", "不能超过 portfolio.leverage"},
		"gross min 为负":        {fixed + "  gross: {min: -1}\n", "不能为负"},
		"equity_bps 超限":       {fixed + "  fees: {equity_bps: 2000}\n", "fees.equity_bps"},
		"funding_column 非法":   {fixed + "  fees: {funding_column: \"a-b\"}\n", "funding_column"},
		"limits 都没写":          {fixed + "portfolio: {limits: {}}\n", "至少写 long_max"},
		"limits 为 0":          {fixed + "portfolio: {limits: {long_max: 0}}\n", "long_max 必须大于 0"},
		"limits 超杠杆":          {fixed + "portfolio: {limits: {short_max: 2}}\n", "short_max 必须大于 0 且不超过"},
		"overflow 不是成员":       {fixed + "portfolio: {limits: {long_max: 0.1, overflow: {to: c}}}\n", "不是池的成员 id"},
		"overflow 缺 to":       {fixed + "portfolio: {limits: {long_max: 0.1, overflow: {}}}\n", "需要写 to"},
	}
	for name, tc := range cases {
		if _, err := parsePool(t, tc.body); err == nil || !strings.Contains(err.Error(), tc.contains) {
			t.Fatalf("%s：期望包含 %q，实际 %v", name, tc.contains, err)
		}
	}
}

func TestLimitsInPlainStrategy(t *testing.T) {
	t.Parallel()
	base := "name: s\nrules:\n  - {id: big, type: rank, score: close, select: {top: 1}, weight: 0.5}\n  - {id: fill, type: rank, score: close, select: {top: 3}, weight: 0.5}\n"
	if _, err := Parse([]byte(base + "portfolio: {limits: {long_max: 0.3, overflow: {to: fill}}}\n")); err != nil {
		t.Fatalf("overflow.to 写规则 id：%v", err)
	}
	if _, err := Parse([]byte(base + "portfolio: {limits: {long_max: 0.3, overflow: {to: other}}}\n")); err == nil || !strings.Contains(err.Error(), "不是规则 id") {
		t.Fatalf("overflow.to 不是规则：%v", err)
	}
}

// 成员方向：写了 switch、exposure、gross 时每个成员都要单方向，switch 只能同方向转移；tranche 按子策略的多空权重判断。
func TestPoolMemberSides(t *testing.T) {
	t.Parallel()
	long := sideMember(t, "market: {select: swap}\n", SideLong)
	short := sideMember(t, "market: {select: swap}\n", SideShort)
	mixed, err := Parse([]byte("name: m\nbar: 1h\nmarket: {select: swap}\nrules:\n  - {id: l, type: rank, score: close, select: {top: 1}, weight: 0.5}\n  - {id: s, type: rank, score: close, select: {top: 1}, weight: 0.5, side: short}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if long.Direction() != SideLong || short.Direction() != SideShort || mixed.Direction() != "" {
		t.Fatalf("方向判定不符：%q %q %q", long.Direction(), short.Direction(), mixed.Direction())
	}
	tranche, err := Parse([]byte(strings.Replace(trancheDSL(trancheRows, ""), "long_weight: 2\n      short_weight: 0", "long_weight: 0\n      short_weight: 1", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if tranche.Direction() != SideShort {
		t.Fatalf("tranche 只有空头子策略时判为空头：%q", tranche.Direction())
	}
	switched, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 1, b: 0}, switch: [{when: {empty: b}, move: {from: a, to: [b]}}]}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(switched, map[string]Strategy{"a": short, "b": short}); err != nil {
		t.Fatalf("同方向转移：%v", err)
	}
	if _, err := ValidatePoolMembers(switched, map[string]Strategy{"a": long, "b": short}); err == nil || !strings.Contains(err.Error(), "同方向") {
		t.Fatalf("跨方向转移应拒绝：%v", err)
	}
	exposure, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 1, b: 0}}\n  exposure: {bands: [{min: 0, max: 0, adjust: reduce_larger}]}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(exposure, map[string]Strategy{"a": long, "b": mixed}); err == nil || !strings.Contains(err.Error(), "单方向") {
		t.Fatalf("混合方向的成员应拒绝：%v", err)
	}
	plain, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 1, b: 0}}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(plain, map[string]Strategy{"a": long, "b": mixed}); err != nil {
		t.Fatalf("不写约束时混合方向的成员照常进池：%v", err)
	}
}

// S8：股票成员不写 market；启用时按池绑定的任务推断，股票与加密成员不能混用，股票池不加杠杆、不写资金费列。
func TestEquityPoolMembers(t *testing.T) {
	t.Parallel()
	pool, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n")
	if err != nil {
		t.Fatal(err)
	}
	stock := sideMember(t, "", SideLong)
	spot := sideMember(t, "market: {select: spot}\n", SideLong)
	inferred, err := InferPoolMemberMarkets(map[string]Strategy{"a": stock, "b": stock}, true)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := ValidatePoolMembers(pool, inferred)
	if err != nil || mode != SingleMarket(MarketEquity) {
		t.Fatalf("股票池：%+v，%v", mode, err)
	}
	if _, err := InferPoolMemberMarkets(map[string]Strategy{"a": stock, "b": spot}, false); err == nil || !strings.Contains(err.Error(), "没有显式写 market") {
		t.Fatalf("绑定加密任务时没写 market 的成员应拒绝：%v", err)
	}
	mixed, err := InferPoolMemberMarkets(map[string]Strategy{"a": stock, "b": spot}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(pool, mixed); err == nil || !strings.Contains(err.Error(), "不能放在同一个池里") {
		t.Fatalf("股票与现货混用应拒绝：%v", err)
	}
	levered, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\nportfolio: {leverage: 2}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(levered, inferred); err == nil || !strings.Contains(err.Error(), "股票池只做多") {
		t.Fatalf("股票池不能加杠杆：%v", err)
	}
	funded, err := parsePool(t, "  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  fees: {funding_column: funding_rate}\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePoolMembers(funded, inferred); err == nil || !strings.Contains(err.Error(), "funding_column") {
		t.Fatalf("不涉及永续时写资金费列应拒绝：%v", err)
	}
	if _, err := ValidatePoolMembers(funded, map[string]Strategy{"a": spot, "b": spot}); err == nil || !strings.Contains(err.Error(), "funding_column") {
		t.Fatalf("纯现货池写资金费列应拒绝：%v", err)
	}
}

// 预热期数计入 hold.max_bars 与日历周期（A 股月按 23 个交易日、周按 5 个；加密月按 31 天、周按 7 天，乘每日根数）。
func TestMaxHoldBarsCountsHoldAndCalendar(t *testing.T) {
	t.Parallel()
	parse := func(rule string) Strategy {
		strategy, err := Parse([]byte("name: h\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1, " + rule + "}\n"))
		if err != nil {
			t.Fatal(err)
		}
		return strategy
	}
	cases := []struct {
		name  string
		rule  string
		clock BarClock
		want  int
	}{
		{"hold.max_bars", "hold: {max_bars: 40}", BarClock{PerDay: 24}, 40},
		{"加密按月", "holding: {calendar: month, days: [1]}", BarClock{PerDay: 24}, 31 * 24},
		{"加密按周", "holding: {calendar: week, days: [1]}", BarClock{PerDay: 24}, 7 * 24},
		{"A 股按月", "holding: {calendar: month, days: [1]}", BarClock{PerDay: 4, Stock: true}, 23 * 4},
		{"A 股按周", "holding: {calendar: week, days: [1]}", BarClock{PerDay: 1, Stock: true}, 5},
		{"按交易日", "holding: {calendar: trading_days, every: 3, days: [1]}", BarClock{PerDay: 4, Stock: true}, 12},
	}
	for _, tc := range cases {
		if got := parse(tc.rule).MaxHoldBars(tc.clock); got != tc.want {
			t.Fatalf("%s：最长持有期 %d，期望 %d", tc.name, got, tc.want)
		}
	}
}
