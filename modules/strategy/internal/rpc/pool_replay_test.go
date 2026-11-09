package rpc

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

var poolReplayTasks = map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}

// replayMembers 返回回放记录里固化的成员 id → DSL 文本。
func replayMembers(t *testing.T, h *harness, replayID string) map[string]string {
	t.Helper()
	stored, err := h.service.Store.GetReplay(h.ctx, replayID)
	if err != nil {
		t.Fatal(err)
	}
	defs, err := input.PoolMemberDefs(stored.MembersJSON)
	if err != nil {
		t.Fatalf("回放记录应带成员快照：%q %v", stored.MembersJSON, err)
	}
	out := map[string]string{}
	for _, def := range defs {
		out[def.ID] = def.DSLYaml
	}
	return out
}

// 从定义发起的池回放固化成员的当前定义；单策略回放不写成员快照。
func TestPoolReplaySnapshotsCurrentMembers(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	h.createStrategy("plain", demoDSL)
	rsp, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: poolReplayTasks, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if members := replayMembers(t, h, rsp.GetReplay().GetReplayId()); members["a"] != spotMemberDSL || members["b"] != swapMemberDSL {
		t.Fatalf("成员快照不符：%v", members)
	}
	stored, _ := h.service.Store.GetReplay(h.ctx, rsp.GetReplay().GetReplayId())
	if len(stored.Tasks) != 2 || stored.Tasks["spot"].FactorTaskID != "ftask_p2" || stored.Tasks["swap"].FactorTaskID != "ftask_p2swap" {
		t.Fatalf("回放应记下池绑定的两个任务：%+v", stored.Tasks)
	}
	// 列表不读成员快照（与 DSL 全文一样）。
	listed, _, err := h.service.Store.ListReplays(h.ctx, "crypto", 0, 10)
	if err != nil || len(listed) != 1 || listed[0].MembersJSON != "" {
		t.Fatalf("列表不带成员快照：%+v %v", listed, err)
	}
	plain, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "plain", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireOK(t, int32(plain.GetRetInfo().GetCode()), plain.GetRetInfo().GetMsg())
	if stored, _ := h.service.Store.GetReplay(h.ctx, plain.GetReplay().GetReplayId()); stored.MembersJSON != "" {
		t.Fatalf("单策略回放不写成员快照：%q", stored.MembersJSON)
	}
}

// 从池实例发起的回放用会话固化的成员版本：实例启用后改了成员定义，回放仍按实例在跑的版本；从定义发起的回放才用新版本。
func TestPoolReplayFromInstanceUsesSessionMembers(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	created := createPoolInstance(h, "p1", "pool_1", poolReplayTasks, true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	changed := strings.Replace(spotMemberDSL, "select: {top: 1}", "select: {top: 2}", 1)
	if rsp, _ := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: "member_spot", DslYaml: changed}); rsp.GetRetInfo().GetCode() != 0 {
		t.Fatal(rsp.GetRetInfo().GetMsg())
	}
	fromInstance, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{InstanceId: "p1", StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireOK(t, int32(fromInstance.GetRetInfo().GetCode()), fromInstance.GetRetInfo().GetMsg())
	if members := replayMembers(t, h, fromInstance.GetReplay().GetReplayId()); members["a"] != spotMemberDSL {
		t.Fatalf("从实例发起应回放会话里的成员版本：%q", members["a"])
	}
	fromDefinition, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: poolReplayTasks, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireOK(t, int32(fromDefinition.GetRetInfo().GetCode()), fromDefinition.GetRetInfo().GetMsg())
	if members := replayMembers(t, h, fromDefinition.GetReplay().GetReplayId()); members["a"] != changed {
		t.Fatalf("从定义发起应回放成员的当前定义：%q", members["a"])
	}
	// 回放排队之后再改成员定义，已固化的快照不变（执行时只读快照，见 replay 包的用例）。
	again := strings.Replace(spotMemberDSL, "select: {top: 1}", "select: {top: 3}", 1)
	if rsp, _ := h.service.UpdateStrategy(h.ctx, &strategypb.UpdateStrategyReq{StrategyId: "member_spot", DslYaml: again}); rsp.GetRetInfo().GetCode() != 0 {
		t.Fatal(rsp.GetRetInfo().GetMsg())
	}
	if members := replayMembers(t, h, fromDefinition.GetReplay().GetReplayId()); members["a"] != changed {
		t.Fatalf("排队期间改成员定义不影响已固化的快照：%q", members["a"])
	}
}

const driftPoolDSL = `name: drift_pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: member_spot}
    - {id: b, strategy: member_swap}
  allocation:
    method: drift
    ratios: {a: 0.6, b: 0.4}
`

// drift 池写了漂移起点时，回放的起点必须等于它；没写起点时任意起点都行。
func TestPoolReplayStartMustEqualDriftStart(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("drift_free", driftPoolDSL)
	h.createStrategy("drift_start", driftPoolDSL+"    start: \"2026-09-01T05:00:00Z\"\n")
	h.createStrategy("drift_initial", driftPoolDSL+"    initial: {at: \"2026-09-01T05:00:00Z\", ratios: {a: 0.5, b: 0.5}}\n")
	start := func(strategyID, from string) *strategypb.StartReplayRsp {
		rsp, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: strategyID, FactorTasks: poolReplayTasks, StartTime: from, EndTime: "2026-09-02T00:00:00Z"})
		return rsp
	}
	free := start("drift_free", "2026-09-01T03:00:00Z")
	requireOK(t, int32(free.GetRetInfo().GetCode()), free.GetRetInfo().GetMsg())
	early := start("drift_start", "2026-09-01T03:00:00Z")
	requireFail(t, int32(early.GetRetInfo().GetCode()), early.GetRetInfo().GetMsg(), "请把 start_time 改为 2026-09-01T05:00:00Z")
	late := start("drift_start", "2026-09-01T06:00:00Z")
	requireFail(t, int32(late.GetRetInfo().GetCode()), late.GetRetInfo().GetMsg(), "strategy_pool.allocation.start")
	exact := start("drift_start", "2026-09-01T05:00:00Z")
	requireOK(t, int32(exact.GetRetInfo().GetCode()), exact.GetRetInfo().GetMsg())
	// 起点在两根之间时，第一根是它之后的那一根，也就是漂移起点。
	between := start("drift_start", "2026-09-01T04:30:00Z")
	requireOK(t, int32(between.GetRetInfo().GetCode()), between.GetRetInfo().GetMsg())
	initialEarly := start("drift_initial", "2026-09-01T03:00:00Z")
	requireFail(t, int32(initialEarly.GetRetInfo().GetCode()), initialEarly.GetRetInfo().GetMsg(), "strategy_pool.allocation.initial.at")
	initialExact := start("drift_initial", "2026-09-01T05:00:00Z")
	requireOK(t, int32(initialExact.GetRetInfo().GetCode()), initialExact.GetRetInfo().GetMsg())
}

// 池回放的非法写法：成员不存在、任务没覆盖成员市场的并集、任务上写资金费列（见 funding_test）都在提交时拒绝，不写回放记录。
func TestPoolReplayRejectsInvalidRequests(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	ghost, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: strings.Replace(poolDefDSL, "member_swap", "ghost", 1), FactorTasks: poolReplayTasks, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireFail(t, int32(ghost.GetRetInfo().GetCode()), ghost.GetRetInfo().GetMsg(), "ghost 不存在")
	oneTask, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: map[string]string{"spot": "ftask_p2"}, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireFail(t, int32(oneTask.GetRetInfo().GetCode()), oneTask.GetRetInfo().GetMsg(), "策略池的成员需要绑定")
	early, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: poolReplayTasks, StartTime: "2025-12-01", EndTime: "2026-01-02"})
	requireFail(t, int32(early.GetRetInfo().GetCode()), early.GetRetInfo().GetMsg(), "")
	listed, _, err := h.service.Store.ListReplays(h.ctx, "crypto", 0, 10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("被拒绝的回放不写记录：%+v %v", listed, err)
	}
}
