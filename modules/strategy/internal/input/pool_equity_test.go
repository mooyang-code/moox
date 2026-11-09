package input

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

func undeclaredMember(t *testing.T, id string) PoolMemberDef {
	t.Helper()
	raw := "name: " + id + "\n" + poolMemberRules
	return PoolMemberDef{ID: id, StrategyID: "strategy_" + id, DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Strategy: parseStrategy(t, raw)}
}

// S8：池绑定 equity 任务时，没写 market 的成员按股票成员解析；绑定加密任务时拒绝。
func TestResolvePoolInfersEquityMembers(t *testing.T) {
	client := newFakeClient("equity")
	client.tagMarkets["binance_equity"] = "equity"
	members := []PoolMemberDef{undeclaredMember(t, "a"), undeclaredMember(t, "b")}
	pooled, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, poolDSL), members)
	if err != nil {
		t.Fatal(err)
	}
	if pooled.Resolved.Market != dsl.SingleMarket(dsl.MarketEquity) || pooled.Members[0].Resolved.Market != dsl.SingleMarket(dsl.MarketEquity) {
		t.Fatalf("股票池与成员的市场：%+v / %+v", pooled.Resolved.Market, pooled.Members[0].Resolved.Market)
	}
	if _, err := ResolvePool(context.Background(), newFakeClient("spot"), "space", []string{"ftask_1h"}, parseStrategy(t, poolDSL), members); err == nil || !strings.Contains(err.Error(), "没有显式写 market") {
		t.Fatalf("绑定加密任务时没写 market 的成员应拒绝：%v", err)
	}
}

// 写了 fees.funding_column 时永续的 View 必须有这一列；成员不涉及永续时拒绝。
func TestResolvePoolChecksFundingColumn(t *testing.T) {
	funded := strings.Replace(poolDSL, "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}", "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  fees: {funding_column: funding_rate}", 1)
	members := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}
	client := newDualClient()
	if _, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, funded), members); err == nil || !strings.Contains(err.Error(), "没有资金费列 funding_rate") {
		t.Fatalf("永续 View 没有资金费列应拒绝：%v", err)
	}
	swap := client.views["view_factor_swap1h"]
	swap.Columns = append(append([]ViewColumn(nil), swap.Columns...), plainColumn("funding_rate"))
	client.views["view_factor_swap1h"] = swap
	if _, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, funded), members); err != nil {
		t.Fatalf("永续 View 有资金费列（现货 View 没有）时可以启用：%v", err)
	}
	spotOnly := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "spot")}
	if _, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, funded), spotOnly); err == nil || !strings.Contains(err.Error(), "funding_column") {
		t.Fatalf("成员不涉及永续时应拒绝：%v", err)
	}
}
