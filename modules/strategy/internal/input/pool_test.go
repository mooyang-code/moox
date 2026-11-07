package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

const poolDSL = `name: pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: strategy_a}
    - {id: b, strategy: strategy_b}
  allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}
`

const poolMemberRules = `rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 1}
    weight: 1
portfolio: {max_missing: 1}
`

func memberDef(t *testing.T, id, market string) PoolMemberDef {
	t.Helper()
	raw := "name: " + id + "\nmarket: {select: " + market + "}\n" + poolMemberRules
	return PoolMemberDef{ID: id, StrategyID: "strategy_" + id, DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Strategy: parseStrategy(t, raw)}
}

func TestResolvePoolBindsUnionOfMemberMarkets(t *testing.T) {
	client := newDualClient()
	members := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}
	pooled, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, poolDSL), members)
	if err != nil {
		t.Fatal(err)
	}
	if pooled.Resolved.Market.Select != dsl.MarketBoth || len(pooled.Resolved.Tasks) != 2 || pooled.Program == nil || !pooled.Program.Strategy.IsPool() {
		t.Fatalf("池的绑定不符：%+v", pooled.Resolved)
	}
	if len(pooled.Members) != 2 || len(pooled.Programs) != 2 {
		t.Fatalf("成员数不符：%d", len(pooled.Members))
	}
	spot, swap := pooled.Members[0].Resolved, pooled.Members[1].Resolved
	if spot.Market != dsl.SingleMarket(dsl.MarketSpot) || len(spot.Tasks) != 1 || spot.Tasks["spot"].ViewID != "view_factor_1h" {
		t.Fatalf("现货成员只绑现货任务：%+v", spot)
	}
	if swap.Market != dsl.SingleMarket(dsl.MarketSwap) || len(swap.Tasks) != 1 || swap.Tasks["swap"].ViewID != "view_factor_swap1h" {
		t.Fatalf("永续成员只绑永续任务：%+v", swap)
	}
	if len(pooled.Resolved.Factors) == 0 || pooled.Resolved.Factors["ma"] == "" {
		t.Fatalf("池的因子契约是成员的并集：%+v", pooled.Resolved.Factors)
	}
	raw, err := pooled.MembersJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, programs, err := ParseMembers(raw)
	if err != nil || len(parsed) != 2 || len(programs) != 2 || parsed[1].ID != "b" || parsed[1].DSLYaml != members[1].DSLYaml || parsed[1].DSLHash != members[1].DSLHash {
		t.Fatalf("members_json 往返失败：%+v %v", parsed, err)
	}
}

func TestResolvePoolRejectsInconsistentBindings(t *testing.T) {
	cases := map[string]struct {
		prepare func(*fakeClient)
		tasks   []string
		members []PoolMemberDef
		want    string
	}{
		"少绑一个市场": {func(*fakeClient) {}, []string{"ftask_1h"}, []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}, "实际绑定了 spot"},
		"多绑一个市场": {func(*fakeClient) {}, []string{"ftask_1h", "ftask_swap1h"}, []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "spot")}, "实际绑定了 spot、swap"},
		"成员定义缺失": {func(*fakeClient) {}, []string{"ftask_1h"}, []PoolMemberDef{memberDef(t, "a", "spot")}, "不存在"},
		"View 没有 close": {func(c *fakeClient) {
			v := c.views["view_factor_swap1h"]
			v.Columns = []ViewColumn{factorColumn("ma_20", "ma")}
			c.views["view_factor_swap1h"] = v
		}, []string{"ftask_1h", "ftask_swap1h"}, []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}, "没有 close 列"},
		"成员缺列": {func(c *fakeClient) {
			v := c.views["view_factor_swap1h"]
			v.Columns = []ViewColumn{plainColumn("close")}
			c.views["view_factor_swap1h"] = v
		}, []string{"ftask_1h", "ftask_swap1h"}, []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}, "成员 b：列 ma_20 不存在于 View view_factor_swap1h"},
	}
	for name, tc := range cases {
		client := newDualClient()
		tc.prepare(client)
		if _, err := ResolvePool(context.Background(), client, "space", tc.tasks, parseStrategy(t, poolDSL), tc.members); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：期望错误含 %q，实际 %v", name, tc.want, err)
		}
	}
	if _, _, err := Resolve(context.Background(), newDualClient(), "space", []string{"ftask_1h"}, parseStrategy(t, poolDSL)); err == nil || !strings.Contains(err.Error(), "策略池") {
		t.Fatalf("策略池不能按普通策略解析：%v", err)
	}
}

func TestResolvePoolChecksRetentionCoversWarmUp(t *testing.T) {
	// 新建的数据集保留 720h（1h 周期即 720 根）：择时窗口 1000 期要预热 1001 + 1 根，装不下。
	raw := strings.Replace(poolDSL, "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}", "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  timing: {window: 1000}", 1)
	members := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}
	_, err := ResolvePool(context.Background(), newDualClient(), "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, raw), members)
	if err == nil || !strings.Contains(err.Error(), "保留 720 根，策略池预热需要 1002 根") {
		t.Fatalf("保留期装不下预热应被拒绝：%v", err)
	}
	// 窗口 100：预热 101 + 1 根，加上回看 1 根，装得下。
	raw = strings.Replace(raw, "window: 1000", "window: 100", 1)
	if _, err := ResolvePool(context.Background(), newDualClient(), "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, raw), members); err != nil {
		t.Fatal(err)
	}
}

// 启用时按精确的覆盖统计核对 View 实际的历史：配置的保留期够，但历史还没攒够预热需要的根数时拒绝。
func TestCheckPoolWarmCoverageUsesActualHistory(t *testing.T) {
	members := []PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap")}
	resolve := func(window string) (PoolResolved, *fakeClient) {
		client := newDualClient()
		raw := strings.Replace(poolDSL, "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}", "allocation: {method: fixed, ratios: {a: 0.5, b: 0.5}}\n  timing: {window: "+window+"}", 1)
		pooled, err := ResolvePool(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, raw), members)
		if err != nil {
			t.Fatal(err)
		}
		return pooled, client
	}
	now := barStart.Add(time.Hour + 30*time.Minute) // 最新已收盘的一根就是索引里最晚的那根（barStart）
	pooled, client := resolve("100")
	if err := CheckPoolWarmCoverage(context.Background(), client, "space", pooled, now); err != nil {
		t.Fatalf("历史 500 根够预热 101 根：%v", err)
	}
	// 保留期 720 根装得下 601 根，但 View 实际只攒了 500 根。
	pooled, client = resolve("600")
	err := CheckPoolWarmCoverage(context.Background(), client, "space", pooled, now)
	var early *EarlyStartError
	if !errors.As(err, &early) || !strings.Contains(err.Error(), "不能启用") || !strings.Contains(err.Error(), "成员 a") {
		t.Fatalf("历史不够应拒绝启用并说明成员：%v", err)
	}
	// 覆盖未知（View 还没有数据）时无法确认，同样拒绝。
	pooled, client = resolve("100")
	for _, id := range []string{"view_factor_1h", "view_factor_swap1h"} {
		view := client.views[id]
		view.IndexedFrom, view.IndexedTo = time.Time{}, time.Time{}
		client.views[id] = view
	}
	if err := CheckPoolWarmCoverage(context.Background(), client, "space", pooled, now); err == nil {
		t.Fatal("覆盖范围未知应拒绝")
	}
	service := Service{Client: client}
	pooled, client = resolve("600")
	service.Client = client
	if err := service.CheckPoolCoverage(context.Background(), "space", pooled, now); err == nil {
		t.Fatal("服务的启用校验也要包含预热历史")
	}
}
