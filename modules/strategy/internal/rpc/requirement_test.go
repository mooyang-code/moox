package rpc

import (
	"reflect"
	"strings"
	"testing"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 建实例要绑定的任务由后端从 DSL 推出：写了 market 的给出主帧市场、参考市场与 来源名:市场；没写 market 的只说明市场待定；
// 策略池取成员市场的并集，成员不存在、股票成员与现货永续成员混放记问题。ValidateStrategy 与 GetStrategy 都带上。
func TestStrategyTaskRequirement(t *testing.T) {
	h := newHarness(t)
	validate := func(raw string) *strategypb.StrategyTaskRequirement {
		t.Helper()
		rsp, _ := h.service.ValidateStrategy(h.ctx, &strategypb.ValidateStrategyReq{DslYaml: raw})
		requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
		return rsp.GetRequirement()
	}
	keys := func(requirement *strategypb.StrategyTaskRequirement) string {
		return strings.Join(requirement.GetTaskKeys(), ",")
	}

	dual := "name: d\nbar: 1h\nmarket: {select: both, prefer: swap}\nsources:\n  daily: {bar: 1d}\nrules:\n  - {id: r, type: rank, score: daily.close, select: {top: 1}, weight: 1}\n"
	got := validate(dual)
	if !got.GetMarketDeclared() || got.GetPool() || keys(got) != "spot,swap,daily:spot,daily:swap" || len(got.GetProblems()) != 0 {
		t.Fatalf("双市场带来源：%+v", got)
	}
	if !reflect.DeepEqual(got.GetSources(), []*strategypb.StrategySourceRequirement{{Name: "daily", Bar: "1d"}}) {
		t.Fatalf("来源：%+v", got.GetSources())
	}
	reference := "name: r\nbar: 1h\nmarket: {select: swap, reference: [spot]}\nuniverse: {min_age_bars: 3, age_markets: all}\nrules:\n  - {id: r, type: rank, score: close, select: {top: 1}, weight: 1}\n"
	if got := validate(reference); keys(got) != "swap,spot" || strings.Join(got.GetReferenceMarkets(), ",") != "spot" {
		t.Fatalf("参考市场：%+v", got)
	}
	if got := validate(demoDSL); len(got.GetReferenceMarkets()) != 0 {
		t.Fatalf("没写参考市场时为空：%+v", got)
	}
	if got := validate(demoDSL); got.GetMarketDeclared() || len(got.GetTaskKeys()) != 0 {
		t.Fatalf("没写 market 时市场待定：%+v", got)
	}

	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	pool := h.createStrategy("pool_1", poolDefDSL)
	if got := validate(poolDefDSL); !got.GetPool() || got.GetTuned() || got.GetPoolAllocationMethod() != "fixed" || got.GetPoolDriftStart() != "" || keys(got) != "spot,swap" {
		t.Fatalf("策略池取成员市场的并集与分配方式：%+v", got)
	}
	stored, _ := h.service.GetStrategy(h.ctx, &strategypb.GetStrategyReq{StrategyId: pool.GetStrategyId()})
	if keys(stored.GetRequirement()) != "spot,swap" || stored.GetRequirement().GetPoolAllocationMethod() != "fixed" {
		t.Fatalf("GetStrategy 也带上：%+v", stored.GetRequirement())
	}
	// 调参策略带出 tuned=true。
	tuned := "name: tuned\nfactor_params:\n  ma__var:\n    window: \"20\"\nrules:\n  - {id: r, score: close, select: {top: 1}, weight: 1}\n"
	if got := validate(tuned); !got.GetTuned() || got.GetPool() || got.GetPoolAllocationMethod() != "" || got.GetPoolDriftStart() != "" {
		t.Fatalf("调参策略应带出 tuned=true：%+v", got)
	}
	// 策略池 drift 带出 pool_allocation_method=drift 与 pool_drift_start（start 或 initial.at）。
	driftStart := "name: drift_start\nbar: 1h\nstrategy_pool:\n  members:\n    - {id: a, strategy: member_spot}\n  allocation: {method: drift, ratios: {a: 1}, start: \"2026-01-01T00:00:00Z\"}\n"
	if got := validate(driftStart); !got.GetPool() || got.GetPoolAllocationMethod() != "drift" || got.GetPoolDriftStart() != "2026-01-01T00:00:00Z" {
		t.Fatalf("drift start 应带出分配方式与漂移起点：%+v", got)
	}
	driftInitial := "name: drift_init\nbar: 1h\nstrategy_pool:\n  members:\n    - {id: a, strategy: member_spot}\n  allocation: {method: drift, ratios: {a: 1}, initial: {at: \"2026-02-01\", ratios: {a: 1}}}\n"
	if got := validate(driftInitial); !got.GetPool() || got.GetPoolAllocationMethod() != "drift" || got.GetPoolDriftStart() != "2026-02-01" {
		t.Fatalf("drift initial.at 应带出分配方式与漂移起点：%+v", got)
	}
	h.createStrategy("member_stock", "name: stock_member\nbar: 1h\n"+poolRules)
	mixed := strings.Replace(poolDefDSL, "member_swap", "member_stock", 1)
	if got := validate(mixed); !strings.Contains(strings.Join(got.GetProblems(), ""), "股票成员不能与现货、永续成员放在同一个池里") || len(got.GetTaskKeys()) != 0 || got.GetPoolAllocationMethod() != "fixed" {
		t.Fatalf("混放股票成员：%+v", got)
	}
	if err := h.service.Store.SoftDeleteDefinition(h.ctx, "member_swap", h.now); err != nil {
		t.Fatal(err)
	}
	stored, _ = h.service.GetStrategy(h.ctx, &strategypb.GetStrategyReq{StrategyId: pool.GetStrategyId()})
	if !strings.Contains(strings.Join(stored.GetRequirement().GetProblems(), ""), "member_swap 不存在") {
		t.Fatalf("成员定义被删：%+v", stored.GetRequirement())
	}
}

// 实例返回关联策略定义的名称（strategy_name），覆盖创建、详情与列表接口。
func TestStrategyInstanceReturnsStrategyName(t *testing.T) {
	h := newHarness(t)
	strategy := h.createStrategy("s_named", demoDSL)
	createdRsp := h.createInstance("inst_named", strategy.GetStrategyId(), "ftask_a", "", false)
	requireOK(t, int32(createdRsp.GetRetInfo().GetCode()), createdRsp.GetRetInfo().GetMsg())
	created := createdRsp.GetInstance()
	if created.GetStrategyName() != strategy.GetName() || created.GetStrategyName() != "demo" {
		t.Fatalf("CreateStrategyInstance 应返回 strategy_name=demo，实际 %q", created.GetStrategyName())
	}
	got, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: created.GetInstanceId()})
	requireOK(t, int32(got.GetRetInfo().GetCode()), got.GetRetInfo().GetMsg())
	if got.GetInstance().GetStrategyName() != "demo" {
		t.Fatalf("GetStrategyInstance 应返回 strategy_name=demo，实际 %q", got.GetInstance().GetStrategyName())
	}
	listed, _ := h.service.ListStrategyInstances(h.ctx, &strategypb.ListStrategyInstancesReq{})
	requireOK(t, int32(listed.GetRetInfo().GetCode()), listed.GetRetInfo().GetMsg())
	if len(listed.GetInstances()) != 1 || listed.GetInstances()[0].GetStrategyName() != "demo" {
		t.Fatalf("ListStrategyInstances 应返回 strategy_name=demo：%+v", listed.GetInstances())
	}
}
