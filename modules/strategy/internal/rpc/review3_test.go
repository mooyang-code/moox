package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 账户已被其他启用实例持有时，在写会话之前就拒绝：实例上不留下待定会话，之后仍可直接修改或删除。
func TestEnableHolderCheckLeavesNoPendingSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "acct-1", true)
	h.createInstance("i2", "s1", "view_a", "acct-1", false)
	rsp := h.setEnabled("i2", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "已被启用实例 i1 绑定")
	instance, err := h.service.Store.GetInstance(h.ctx, "i2")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := h.service.Store.ListSessions(h.ctx, "i2")
	if err != nil {
		t.Fatal(err)
	}
	if instance.SessionID != nil || len(sessions) != 0 {
		t.Fatalf("被拒绝的启用不应留下会话：%+v sessions=%v", instance, sessions)
	}
	deleted, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i2"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
}

// 改绑到已删除的定义被拒绝。
func TestUpdateInstanceRejectsDeletedDefinition(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createStrategy("s2", demoDSL)
	h.createInstance("i1", "s1", "view_a", "", false)
	removed, _ := h.service.DeleteStrategy(h.ctx, &strategypb.DeleteStrategyReq{StrategyId: "s2"})
	requireOK(t, int32(removed.GetRetInfo().GetCode()), removed.GetRetInfo().GetMsg())
	rsp, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s2", ViewId: "view_a"}})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "s2 不存在")
}

// 等锁期间实例被改绑到另一个定义：拿锁后重读发现定义变了，换成新定义的锁再执行，不在旧锁下启用新定义。
func TestEnableFollowsRebindWhileWaitingForLock(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createStrategy("s2", demoDSL+"\n# 第二个定义\n")
	h.createInstance("i1", "s1", "view_a", "", false)
	unlockFirst := h.service.lockStrategy("s1")
	unlockSecond := h.service.lockStrategy("s2")
	done := make(chan *strategypb.SetStrategyInstanceEnabledRsp, 1)
	go func() { done <- h.setEnabled("i1", true) }()
	time.Sleep(50 * time.Millisecond)
	current, err := h.service.Store.GetInstance(h.ctx, "i1")
	if err != nil {
		t.Fatal(err)
	}
	current.StrategyID = "s2"
	current.UpdatedAt = h.now
	if err := h.service.Store.UpdateInstance(h.ctx, current); err != nil {
		t.Fatal(err)
	}
	unlockFirst()
	select {
	case rsp := <-done:
		t.Fatalf("新定义的锁仍被持有时不应完成启用：%v", rsp.GetRetInfo())
	case <-time.After(100 * time.Millisecond):
	}
	unlockSecond()
	rsp := <-done
	requireOK(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg())
	if rsp.GetInstance().GetStrategyId() != "s2" || !rsp.GetInstance().GetEnabled() {
		t.Fatalf("应按改绑后的定义启用：%+v", rsp.GetInstance())
	}
}

// 从实例发起的回放记录来源实例与会话；列表不带 DSL 全文，详情带；起止时间先截到毫秒。
func TestReplayRecordsSourceAndTrimsList(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "", true)
	instance, err := h.service.Store.GetInstance(h.ctx, "i1")
	if err != nil || instance.SessionID == nil {
		t.Fatalf("实例应已启用：%+v err=%v", instance, err)
	}
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{InstanceId: "i1", ViewId: "view_a", StartTime: "2026-09-02T00:00:00.0005Z", EndTime: "2026-09-03T00:00:00Z"})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	replay := started.GetReplay()
	if replay.GetInstanceId() != "i1" || replay.GetSessionId() != *instance.SessionID || replay.GetStartTime() != "2026-09-02T00:00:00Z" {
		t.Fatalf("回放应记录来源并把起点截到毫秒：%+v", replay)
	}
	listed, _ := h.service.ListReplays(h.ctx, &strategypb.ListReplaysReq{})
	if len(listed.GetReplays()) != 1 || listed.GetReplays()[0].GetDslYaml() != "" || listed.GetReplays()[0].GetDslHash() == "" {
		t.Fatalf("列表不应带 DSL 全文但保留版本哈希：%+v", listed.GetReplays())
	}
	detail, _ := h.service.GetReplay(h.ctx, &strategypb.GetReplayReq{ReplayId: replay.GetReplayId()})
	if detail.GetReplay().GetDslYaml() == "" {
		t.Fatal("详情应带 DSL 全文")
	}
	stored, err := h.service.Store.GetReplay(h.ctx, replay.GetReplayId())
	if err != nil || stored.Status != store.ReplayPending {
		t.Fatalf("回放应排队等待执行：%+v err=%v", stored, err)
	}
}
