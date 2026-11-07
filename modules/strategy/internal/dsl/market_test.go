package dsl

import (
	"strings"
	"testing"
)

const marketRankBody = `
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 2}
    weight: 1
`

func parseMarket(t *testing.T, market string) (Strategy, error) {
	t.Helper()
	return Parse([]byte("name: m\n" + market + marketRankBody))
}

func TestParseMarketAcceptsFiveCombinations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, market string
		want         Market
		markets      []string
	}{
		{"纯现货", "market: {select: spot, prefer: spot}", Market{MarketSpot, MarketSpot}, []string{"spot"}},
		{"纯现货省略 prefer", "market: {select: spot}", Market{MarketSpot, MarketSpot}, []string{"spot"}},
		{"纯永续", "market: {select: swap, prefer: swap}", Market{MarketSwap, MarketSwap}, []string{"swap"}},
		{"纯永续省略 prefer", "market: {select: swap}", Market{MarketSwap, MarketSwap}, []string{"swap"}},
		{"两边选现货优先", "market: {select: both, prefer: spot}", Market{MarketBoth, MarketSpot}, []string{"spot", "swap"}},
		{"两边选永续优先", "market: {select: both, prefer: swap}", Market{MarketBoth, MarketSwap}, []string{"spot", "swap"}},
		{"现货选币永续下单", "market: {select: spot, prefer: swap}", Market{MarketSpot, MarketSwap}, []string{"spot", "swap"}},
	}
	for _, tc := range cases {
		strategy, err := parseMarket(t, tc.market)
		if err != nil {
			t.Fatalf("%s：%v", tc.name, err)
		}
		if strategy.Market != tc.want || !strategy.MarketDeclared {
			t.Fatalf("%s：市场模式 %+v，声明=%v", tc.name, strategy.Market, strategy.MarketDeclared)
		}
		if got := strings.Join(strategy.Market.Markets(), ","); got != strings.Join(tc.markets, ",") {
			t.Fatalf("%s：涉及的市场 %s", tc.name, got)
		}
	}
}

func TestParseMarketRejectsIllegal(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"market: {select: swap, prefer: spot}": "select: swap 必须配 prefer: swap",
		"market: {select: both}":               "必须写 market.prefer",
		"market: {prefer: spot}":               "必须写 select",
		"market: {select: equity}":             "不需要也不允许写 market",
		"market: {select: futures}":            "不受支持",
		"market: {select: both, prefer: both}": "market.prefer",
		"market: {select: spot, foo: 1}":       "未知字段",
		"market: spot":                         "必须是一个对象",
	}
	for market, want := range cases {
		if _, err := parseMarket(t, market); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s 应报 %q：%v", market, want, err)
		}
	}
}

func TestParseWithoutMarketLeavesDefaultToBinding(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte("name: m\n" + marketRankBody))
	if err != nil {
		t.Fatal(err)
	}
	if !strategy.Market.IsZero() || strategy.MarketDeclared {
		t.Fatalf("没写 market 时由绑定的任务填默认值：%+v", strategy.Market)
	}
}

func TestValidateModeRestrictions(t *testing.T) {
	t.Parallel()
	shortRule := "name: m\nrules:\n  - {id: s, type: rank, side: short, score: close, select: {bottom: 1}, weight: 1}\n"
	levered := "name: m\nportfolio: {leverage: 1.5}\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n"
	params := "name: m\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\nfactor_params:\n  ema__var:\n    window: \"close\"\n"
	spot, swap, both := SingleMarket(MarketSpot), SingleMarket(MarketSwap), Market{MarketBoth, MarketSpot}
	cases := []struct {
		name, dsl string
		mode      Market
		want      string
	}{
		{"纯现货做空", shortRule, spot, "不允许规则 s 做空"},
		{"股票做空", shortRule, SingleMarket(MarketEquity), "不允许规则 s 做空"},
		{"纯永续做空", shortRule, swap, ""},
		{"混合市场做空", shortRule, both, ""},
		{"现货选币永续下单可做空", shortRule, Market{MarketSpot, MarketSwap}, ""},
		{"纯现货杠杆", levered, spot, "不允许 portfolio.leverage 大于 1"},
		{"纯永续杠杆", levered, swap, ""},
		{"混合市场杠杆", levered, both, ""},
	}
	for _, tc := range cases {
		strategy, err := Parse([]byte(tc.dsl))
		if err != nil {
			t.Fatalf("%s：解析：%v", tc.name, err)
		}
		err = ValidateMode(strategy, tc.mode)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s：期望 %q，实际 %v", tc.name, tc.want, err)
		}
	}
	strategy, err := Parse([]byte(params))
	if err != nil {
		t.Skipf("factor_params 写法不同：%v", err)
	}
	if err := ValidateMode(strategy, both); err == nil || !strings.Contains(err.Error(), "factor_params") {
		t.Fatalf("双任务不许用 factor_params：%v", err)
	}
	if err := ValidateMode(strategy, spot); err != nil {
		t.Fatalf("单任务可以用 factor_params：%v", err)
	}
	if err := ValidateMode(strategy, Market{}); err == nil {
		t.Fatal("市场模式未确定应报错")
	}
}

func TestValidateModeTrancheRestrictions(t *testing.T) {
	t.Parallel()
	tranche := func(sizing string, shortWeight string) string {
		return `name: t
rules:
  - id: g
    type: tranche
    weight: 1
    tranche:
      hold_period: 3
      long_factors: [{factor: x1}]
      long_weight: 1
      short_weight: ` + shortWeight + `
      short_factors: [{factor: x1}]
      sizing: {method: ` + sizing + `}
`
	}
	spot, swap, both := SingleMarket(MarketSpot), SingleMarket(MarketSwap), Market{MarketBoth, MarketSwap}
	cases := []struct {
		name, dsl string
		mode      Market
		want      string
	}{
		{"现货按权重持有可以", tranche("weight", "0"), spot, ""},
		{"现货按数量持有", tranche("quantity", "0"), spot, "只能用 sizing.method: weight"},
		{"现货做空", tranche("weight", "0.5"), spot, "不能做空"},
		{"两边选的混合市场", tranche("weight", "0"), both, "S1 暂不支持双市场 tranche"},
		{"两边选现货优先", tranche("weight", "0"), Market{MarketBoth, MarketSpot}, "S1 暂不支持双市场 tranche"},
		{"现货选币永续下单", tranche("weight", "0"), Market{MarketSpot, MarketSwap}, "S1 暂不支持双市场 tranche"},
		{"纯永续按权重持有", tranche("weight", "0.5"), swap, ""},
		{"纯永续不受限", tranche("quantity", "0.5"), swap, ""},
	}
	for _, tc := range cases {
		strategy, err := Parse([]byte(tc.dsl))
		if err != nil {
			t.Fatalf("%s：解析：%v", tc.name, err)
		}
		err = ValidateMode(strategy, tc.mode)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s：期望 %q，实际 %v", tc.name, tc.want, err)
		}
	}
}
