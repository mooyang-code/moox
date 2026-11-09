package dsl

import (
	"strings"
	"testing"
)

// marketRule 构造一份写了策略级 market 与一条规则的 DSL；rule 是规则里 id、type、score 之外的字段。
func marketRule(market, rule string) string {
	return "name: x\nmarket: " + market + "\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 0.5, " + rule + "}\n"
}

// S4 规则级市场：两个市场的模式下可写 spot 或 swap，单市场模式下可写与策略相同的市场；RuleMarket 只在两个市场时生效。
func TestRuleMarketAccepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, market, rule, want string
		effective                string
	}{
		{"两边选、现货规则", "{select: both, prefer: swap}", "market: spot", MarketSpot, MarketSpot},
		{"两边选、永续空头规则", "{select: both, prefer: spot}", "market: swap, side: short", MarketSwap, MarketSwap},
		{"现货选币永续下单、永续规则", "{select: spot, prefer: swap}", "market: swap", MarketSwap, MarketSwap},
		{"纯永续写同一市场", "{select: swap}", "market: swap, side: short", MarketSwap, ""},
		{"纯现货写同一市场", "{select: spot}", "market: spot", MarketSpot, ""},
	}
	for _, tc := range cases {
		strategy, err := Parse([]byte(marketRule(tc.market, tc.rule)))
		if err != nil {
			t.Fatalf("%s：%v", tc.name, err)
		}
		rule := strategy.Rules[0]
		if rule.Market != tc.want || RuleMarket(strategy.Market, rule) != tc.effective || !strategy.HasRuleMarket() {
			t.Fatalf("%s：market=%q 生效=%q", tc.name, rule.Market, RuleMarket(strategy.Market, rule))
		}
	}
	plain, err := Parse([]byte(marketRule("{select: both, prefer: spot}", "side: long")))
	if err != nil || plain.HasRuleMarket() || RuleMarket(plain.Market, plain.Rules[0]) != "" {
		t.Fatalf("不写规则级 market 时应与现状一致：%v", err)
	}
}

// S4 规则级市场的保存校验：非法取值、对象写法、策略没写 market、单市场不一致、现货规则做空。
func TestRuleMarketRejected(t *testing.T) {
	t.Parallel()
	runOptionCases(t, map[string]optionCase{
		"取值 both":     {marketRule("{select: both, prefer: spot}", "market: both"), "规则级 market 只能是 spot 或 swap"},
		"取值 equity":   {marketRule("{select: both, prefer: spot}", "market: equity"), "规则级 market 只能是 spot 或 swap"},
		"对象写法":        {marketRule("{select: both, prefer: spot}", "market: {select: spot}"), "market 必须是字符串"},
		"空值":          {marketRule("{select: both, prefer: spot}", "market: ''"), "market 不能为空"},
		"策略没写 market": {"name: x\nrules:\n  - {id: a, type: rank, score: close, select: {top: 1}, weight: 1, market: spot}\n", "策略也必须显式写 market"},
		"纯现货写永续":      {marketRule("{select: spot}", "market: swap"), "单市场策略的规则 market 必须与策略相同"},
		"纯永续写现货":      {marketRule("{select: swap}", "market: spot"), "单市场策略的规则 market 必须与策略相同"},
		"现货规则做空":      {marketRule("{select: both, prefer: swap}", "market: spot, side: short"), "不能做空"},
	})
}

// 单市场模式（DSL 没写 market、按绑定任务确定的模式）在启用时做一致性检查。
func TestValidateModeRuleMarket(t *testing.T) {
	t.Parallel()
	strategy, err := Parse([]byte(marketRule("{select: both, prefer: spot}", "market: swap")))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMode(strategy, SingleMarket(MarketEquity)); err == nil || !strings.Contains(err.Error(), "必须与策略相同") {
		t.Fatalf("股票模式下规则级 market 应拒绝：%v", err)
	}
	if err := ValidateMode(strategy, SingleMarket(MarketSwap)); err != nil {
		t.Fatalf("纯永续模式下 market: swap 应允许：%v", err)
	}
}
