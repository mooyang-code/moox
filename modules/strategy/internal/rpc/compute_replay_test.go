package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 发起回放时的输入来源：不写按 view；声明了 factor_params 的策略必须用 compute；compute 记进回放并在详情里返回，
// 终点截到最近一根已收盘的 bar 之前，不查主帧 View 的覆盖范围。
func TestStartReplayInputSource(t *testing.T) {
	h := newHarness(t)
	h.resolver.columns = []string{"close", "bias__var"}
	spot := map[string]string{"spot": "ftask_x"}

	plain, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: demoDSL, FactorTasks: spot, StartTime: "2026-09-01", EndTime: "2026-09-02"})
	requireOK(t, int32(plain.GetRetInfo().GetCode()), plain.GetRetInfo().GetMsg())
	if plain.GetReplay().GetInput() != store.ReplayInputView {
		t.Fatalf("不写来源按 view：%+v", plain.GetReplay())
	}
	// 之后发起的回放晚 1 秒登记：列表与认领按登记时间排序。
	h.now = h.now.Add(time.Second)

	bogus, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: demoDSL, FactorTasks: spot, StartTime: "2026-09-01", EndTime: "2026-09-02", Input: "storage"})
	requireFail(t, int32(bogus.GetRetInfo().GetCode()), bogus.GetRetInfo().GetMsg(), "input")

	viewTuned, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: tunedDSL, FactorTasks: spot, StartTime: "2026-09-01", EndTime: "2026-09-02", Input: "view"})
	requireFail(t, int32(viewTuned.GetRetInfo().GetCode()), viewTuned.GetRetInfo().GetMsg(), "input=compute")

	// 区间超出 View 覆盖（2026-09-30 23:00）也能发起：终点只截到 now（2026-10-01 00:00）之前最近一根已收盘的 bar。
	tuned, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{DslYaml: tunedDSL, FactorTasks: spot, StartTime: "2026-09-30T20:00:00Z", EndTime: "2026-10-03T00:00:00Z", Input: "compute"})
	requireOK(t, int32(tuned.GetRetInfo().GetCode()), tuned.GetRetInfo().GetMsg())
	if tuned.GetReplay().GetInput() != store.ReplayInputCompute || tuned.GetBarCount() != 3 || tuned.GetLastBarEnd() != "2026-09-30T23:00:00Z" {
		t.Fatalf("因子现算的回放不符：%+v bars=%d last=%s", tuned.GetReplay(), tuned.GetBarCount(), tuned.GetLastBarEnd())
	}
	stored, err := h.service.Store.GetReplay(h.ctx, tuned.GetReplay().GetReplayId())
	if err != nil || stored.Input != store.ReplayInputCompute || stored.Tasks["spot"].ViewGeneration == "" {
		t.Fatalf("回放记录应带上来源与 View 代次：%+v err=%v", stored, err)
	}
	listed, _ := h.service.ListReplays(h.ctx, &strategypb.ListReplaysReq{})
	if len(listed.GetReplays()) != 2 || listed.GetReplays()[0].GetInput() != store.ReplayInputCompute {
		t.Fatalf("列表也应带上来源：%+v", listed.GetReplays())
	}
	claimed, found, err := h.service.Store.ClaimNextReplay(h.ctx, h.now)
	if err != nil || !found || claimed.Input != store.ReplayInputView {
		t.Fatalf("先排队的是 view 回放：%+v found=%v err=%v", claimed, found, err)
	}
	claimed, found, err = h.service.Store.ClaimNextReplay(h.ctx, h.now.Add(time.Second))
	if err != nil || !found || claimed.Input != store.ReplayInputCompute {
		t.Fatalf("执行器认领时要读到 compute：%+v found=%v err=%v", claimed, found, err)
	}
}

// “因子现算”的限制：策略池、股票市场都拒绝（现货加永续两个主帧任务可以）；一期的因子帧估算超过单步上限时提交即拒绝。
func TestStartComputeReplayRejections(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	day := func(req *strategypb.StartReplayReq) *strategypb.StartReplayReq {
		req.StartTime, req.EndTime, req.Input = "2026-09-01", "2026-09-02", "compute"
		return req
	}
	// 现货加永续两个主帧任务可以现算（每个任务各开一个会话）。
	both, _ := h.service.StartReplay(h.ctx, day(&strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a", "swap": "ftask_swap"}}))
	requireOK(t, int32(both.GetRetInfo().GetCode()), both.GetRetInfo().GetMsg())

	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	pooled, _ := h.service.StartReplay(h.ctx, day(&strategypb.StartReplayReq{StrategyId: "pool_1", FactorTasks: map[string]string{"spot": "ftask_a", "swap": "ftask_swap"}}))
	requireFail(t, int32(pooled.GetRetInfo().GetCode()), pooled.GetRetInfo().GetMsg(), "不支持策略池")

	h.resolver.series = 200000
	large, _ := h.service.StartReplay(h.ctx, day(&strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}}))
	requireFail(t, int32(large.GetRetInfo().GetCode()), large.GetRetInfo().GetMsg(), "减少策略引用的列")
	h.resolver.series = 0

	h.resolver.calendar = "cn_stock"
	stock, _ := h.service.StartReplay(h.ctx, day(&strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}}))
	requireFail(t, int32(stock.GetRetInfo().GetCode()), stock.GetRetInfo().GetMsg(), "股票市场")
}
