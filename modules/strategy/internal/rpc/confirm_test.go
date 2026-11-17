package rpc

import (
	"testing"
	"time"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

func (h *harness) confirm(id, session string) *strategypb.ConfirmStrategyInstanceRsp {
	h.t.Helper()
	rsp, err := h.service.ConfirmStrategyInstance(h.ctx, &strategypb.ConfirmStrategyInstanceReq{InstanceId: id, SessionId: session})
	if err != nil {
		h.t.Fatal(err)
	}
	return rsp
}

// 确认实盘（批 4 T4）：挂了账户的实例启用后在试运行，确认后写入确认时间；停用再启用回到试运行，旧会话的确认不能落到新会话上；
// 没挂账户、没启用的实例不能确认。
func TestConfirmStrategyInstanceThenReenableReturnsToTrial(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	created := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true)
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	first := created.GetInstance()
	if first.GetConfirmedAt() != "" {
		t.Fatalf("挂了账户的实例启用后应在试运行：%+v", first)
	}

	h.now = h.now.Add(time.Minute)
	confirmed := h.confirm("i1", first.GetSessionId())
	requireOK(t, int32(confirmed.GetRetInfo().GetCode()), confirmed.GetRetInfo().GetMsg())
	if confirmed.GetInstance().GetConfirmedAt() == "" {
		t.Fatalf("确认后应带确认时间：%+v", confirmed.GetInstance())
	}
	// 再确认一次是成功，确认时间不变。
	h.now = h.now.Add(time.Minute)
	again := h.confirm("i1", "")
	requireOK(t, int32(again.GetRetInfo().GetCode()), again.GetRetInfo().GetMsg())
	if again.GetInstance().GetConfirmedAt() != confirmed.GetInstance().GetConfirmedAt() {
		t.Fatalf("重复确认不应改确认时间：%s → %s", confirmed.GetInstance().GetConfirmedAt(), again.GetInstance().GetConfirmedAt())
	}

	disabled := h.setEnabled("i1", false)
	requireOK(t, int32(disabled.GetRetInfo().GetCode()), disabled.GetRetInfo().GetMsg())
	if disabled.GetInstance().GetConfirmedAt() != "" {
		t.Fatalf("停用应清空确认时间：%+v", disabled.GetInstance())
	}
	stopped := h.confirm("i1", "")
	requireFail(t, int32(stopped.GetRetInfo().GetCode()), stopped.GetRetInfo().GetMsg(), "没有启用")

	reenabled := h.setEnabled("i1", true)
	requireOK(t, int32(reenabled.GetRetInfo().GetCode()), reenabled.GetRetInfo().GetMsg())
	if reenabled.GetInstance().GetConfirmedAt() != "" {
		t.Fatalf("停用再启用应回到试运行：%+v", reenabled.GetInstance())
	}
	stale := h.confirm("i1", first.GetSessionId())
	requireFail(t, int32(stale.GetRetInfo().GetCode()), stale.GetRetInfo().GetMsg(), "已重新启用")
	if got, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: "i1"}); got.GetInstance().GetConfirmedAt() != "" {
		t.Fatal("被拒绝的确认不应写入确认时间")
	}

	h.createStrategy("s2", demoDSL)
	observer := h.createInstance("observer", "s2", "ftask_o", "", true)
	requireOK(t, int32(observer.GetRetInfo().GetCode()), observer.GetRetInfo().GetMsg())
	if observer.GetInstance().GetConfirmedAt() == "" {
		t.Fatalf("没挂账户的实例启用即确认：%+v", observer.GetInstance())
	}
	needless := h.confirm("observer", "")
	requireFail(t, int32(needless.GetRetInfo().GetCode()), needless.GetRetInfo().GetMsg(), "没有挂组合账户")
}

// 预热未完成拒绝确认：策略池实例的会话还没有预热终点时拒绝（预热管理器给出原因时带上原因；进程重启后管理器里没有记录，
// 也按会话里的预热终点拒绝）；预热完成后可以确认。
func TestConfirmRejectedWhileWarmUpIncomplete(t *testing.T) {
	h := newHarness(t)
	warmer := &fakeWarmer{blocked: map[string]string{}}
	h.service.Warmer = warmer
	h.createStrategy("member_spot", spotMemberDSL)
	h.createStrategy("member_swap", swapMemberDSL)
	h.createStrategy("pool_1", poolDefDSL)
	created, err := h.service.CreateStrategyInstance(h.ctx, &strategypb.CreateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{
		InstanceId: "p1", StrategyId: "pool_1", FactorTasks: map[string]string{"spot": "ftask_p2", "swap": "ftask_p2swap"}, LogicalAccountId: "acct-1", Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	session := created.GetInstance().GetSessionId()

	warmer.blocked["p1"] = "策略池预热未完成"
	running := h.confirm("p1", session)
	requireFail(t, int32(running.GetRetInfo().GetCode()), running.GetRetInfo().GetMsg(), "策略池预热未完成")
	warmer.blocked["p1"] = "策略池预热失败，请修复后重新启用实例：保留期不足"
	failed := h.confirm("p1", session)
	requireFail(t, int32(failed.GetRetInfo().GetCode()), failed.GetRetInfo().GetMsg(), "保留期不足")
	// 管理器里没有记录（例如进程刚重启）：仍按会话里没有预热终点拒绝。
	delete(warmer.blocked, "p1")
	restarted := h.confirm("p1", session)
	requireFail(t, int32(restarted.GetRetInfo().GetCode()), restarted.GetRetInfo().GetMsg(), "策略池预热未完成")
	if got, _ := h.service.GetStrategyInstance(h.ctx, &strategypb.GetStrategyInstanceReq{InstanceId: "p1"}); got.GetInstance().GetConfirmedAt() != "" {
		t.Fatal("预热没完成时不应写入确认时间")
	}

	if written, err := h.service.Store.SetSessionWarmed(h.ctx, session, `{}`, h.now.Add(-time.Hour), h.now); err != nil || !written {
		t.Fatalf("写入预热结果：written=%v err=%v", written, err)
	}
	done := h.confirm("p1", session)
	requireOK(t, int32(done.GetRetInfo().GetCode()), done.GetRetInfo().GetMsg())
	if done.GetInstance().GetConfirmedAt() == "" {
		t.Fatalf("预热完成后可以确认：%+v", done.GetInstance())
	}
}
