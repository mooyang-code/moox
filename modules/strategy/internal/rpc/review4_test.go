package rpc

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 改绑与删除等锁期间实例被改绑到第三个定义、且第三个定义正在启用（持有它的锁）：拿锁后重读发现定义变了，
// 换成新定义的锁等启用结束，再按最新状态判断，不能在旧锁下把启用中的实例改绑或删除。
func TestRebindAndDeleteFollowConcurrentRebind(t *testing.T) {
	for name, operate := range map[string]func(h *harness) (int32, string){
		"改绑": func(h *harness) (int32, string) {
			rsp, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s2", ViewId: "view_b"}})
			return int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg()
		},
		"删除": func(h *harness) (int32, string) {
			rsp, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
			return int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg()
		},
	} {
		operate := operate
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.createStrategy("s1", demoDSL)
			h.createStrategy("s2", demoDSL+"\n# 第二个定义\n")
			third := demoDSL + "\n# 第三个定义\n"
			h.createStrategy("s3", third)
			h.createInstance("i1", "s1", "view_a", "", false)
			unlockFirst := h.service.lockStrategy("s1")
			unlockThird := h.service.lockStrategy("s3")
			type result struct {
				code int32
				msg  string
			}
			done := make(chan result, 1)
			go func() {
				code, msg := operate(h)
				done <- result{code, msg}
			}()
			time.Sleep(50 * time.Millisecond)
			// 另一个请求把实例改绑到 s3，随后开始启用：写好会话、挂到实例上，正在联系 Trade。
			current, err := h.service.Store.GetInstance(h.ctx, "i1")
			if err != nil {
				t.Fatal(err)
			}
			current.StrategyID, current.UpdatedAt = "s3", h.now
			if err := h.service.Store.UpdateInstance(h.ctx, current); err != nil {
				t.Fatal(err)
			}
			unlockFirst()
			select {
			case got := <-done:
				t.Fatalf("实例当前定义的锁仍被持有时不应完成：%+v", got)
			case <-time.After(100 * time.Millisecond):
			}
			session := "session-enabling"
			if err := h.service.Store.OpenSession(h.ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: dsl.Hash([]byte(third)), ResolvedJSON: `{"view_id":"view_a"}`, CreatedAt: h.now}, third); err != nil {
				t.Fatal(err)
			}
			if err := h.service.Store.SetInstanceEnabled(h.ctx, "i1", false, &session, nil, h.now); err != nil {
				t.Fatal(err)
			}
			unlockThird()
			got := <-done
			requireFail(t, got.code, got.msg, "")
			after, err := h.service.Store.GetInstance(h.ctx, "i1")
			if err != nil || after.StrategyID != "s3" || after.ViewID != "view_a" || after.DeletedAt != nil || after.SessionID == nil || *after.SessionID != session {
				t.Fatalf("启用中的实例不应被改绑或删除：%+v err=%v", after, err)
			}
		})
	}
}

// 停用对账逐个调用 Trade 期间，另一个实例的会话已被用户的操作换掉：拿锁后重读与列表快照不一致就跳过，
// 不能把正在进行的启用握手当成过期的停用会话释放、清空或关闭。
func TestReconcileDisabledSkipsSessionChangedSinceSnapshot(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	for _, id := range []string{"i0", "i1"} {
		created := h.createInstance(id, "s1", "view_a", "acct-"+id, true)
		requireOK(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg())
	}
	// Trade 释放超时：两个实例都停用，但会话留着等对账。
	h.owner.releaseErr = errors.New("trade timeout")
	for _, id := range []string{"i0", "i1"} {
		h.setEnabled(id, false)
	}
	h.owner.releaseErr = nil
	stale, err := h.service.Store.GetInstance(h.ctx, "i1")
	if err != nil || stale.SessionID == nil {
		t.Fatalf("i1 应保留待释放的会话：%+v err=%v", stale, err)
	}
	staleSession := *stale.SessionID
	replacement := "session-new"
	h.owner.onRelease = func(instance, session string) {
		if instance != "i0" {
			return
		}
		// 对账释放 i0 期间，用户对 i1 重试停用成功，随后开始启用并挂上新会话。
		if err := h.service.Store.ClearInstanceSession(h.ctx, "i1", staleSession, h.now); err != nil {
			t.Error(err)
		}
		_ = h.service.Store.CloseSession(h.ctx, staleSession, h.now)
		if err := h.service.Store.OpenSession(h.ctx, store.Session{SessionID: replacement, InstanceID: "i1", DSLHash: dsl.Hash([]byte(demoDSL)), ResolvedJSON: `{"view_id":"view_a"}`, CreatedAt: h.now}, demoDSL); err != nil {
			t.Error(err)
		}
		if err := h.service.Store.SetInstanceEnabled(h.ctx, "i1", false, &replacement, nil, h.now); err != nil {
			t.Error(err)
		}
	}
	h.owner.releases = nil
	if err := h.service.ReconcileDisabledInstances(h.ctx); err != nil {
		t.Fatal(err)
	}
	for _, released := range h.owner.releases {
		if released == replacement {
			t.Fatalf("不应释放快照之后挂上的新会话：%v", h.owner.releases)
		}
	}
	after, err := h.service.Store.GetInstance(h.ctx, "i1")
	if err != nil || after.SessionID == nil || *after.SessionID != replacement {
		t.Fatalf("新会话应保留在实例上：%+v err=%v", after, err)
	}
	if session, err := h.service.Store.GetSession(h.ctx, replacement); err != nil || session.ClosedAt != nil {
		t.Fatalf("新会话不应被关闭：%+v err=%v", session, err)
	}
	if first, err := h.service.Store.GetInstance(h.ctx, "i0"); err != nil || first.SessionID != nil {
		t.Fatalf("i0 的过期会话应已清空：%+v err=%v", first, err)
	}
}

// 启用时校验 View 能追溯 min_age_bars：不满足时不写会话、不联系 Trade。
func TestEnableChecksAgeCoverageBeforeSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.resolver.coverageErr = errors.New("universe.min_age_bars=240 需要追溯到更早，但 View 当前只覆盖 100 根")
	created := h.createInstance("i1", "s1", "view_a", "acct-1", true)
	requireFail(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg(), "当前只覆盖")
	sessions, err := h.service.Store.ListSessions(h.ctx, "i1")
	if err != nil || len(sessions) != 0 || len(h.owner.claims) != 0 {
		t.Fatalf("覆盖不足时不应写会话或认领：sessions=%v claims=%v err=%v", sessions, h.owner.claims, err)
	}
}

// 解析期间实例被并发改绑（越过锁的路径）：会话挂不上，刚打开的会话立即关闭，也不联系 Trade。
func TestEnableClosesSessionWhenInstanceChangesDuringResolve(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "acct-1", false)
	h.resolver.onResolve = func() {
		h.resolver.onResolve = nil
		current, err := h.service.Store.GetInstance(h.ctx, "i1")
		if err != nil {
			t.Error(err)
			return
		}
		current.ViewID, current.UpdatedAt = "view_b", h.now
		if err := h.service.Store.UpdateInstance(h.ctx, current); err != nil {
			t.Error(err)
		}
	}
	rsp := h.setEnabled("i1", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "被修改")
	sessions, err := h.service.Store.ListSessions(h.ctx, "i1")
	if err != nil || len(sessions) != 1 || sessions[0].ClosedAt == nil {
		t.Fatalf("挂不上的会话应被关闭：%+v err=%v", sessions, err)
	}
	if instance, err := h.service.Store.GetInstance(h.ctx, "i1"); err != nil || instance.SessionID != nil || instance.Enabled {
		t.Fatalf("实例不应挂上会话：%+v err=%v", instance, err)
	}
	if len(h.owner.claims) != 0 {
		t.Fatalf("挂不上会话时不应联系 Trade：%v", h.owner.claims)
	}
	// 之后正常重试能启用到改绑后的 View。
	retried := h.setEnabled("i1", true)
	requireOK(t, int32(retried.GetRetInfo().GetCode()), retried.GetRetInfo().GetMsg())
	if retried.GetInstance().GetViewId() != "view_b" || !retried.GetInstance().GetEnabled() {
		t.Fatalf("重试应启用到改绑后的 View：%+v", retried.GetInstance())
	}
}

// 回放记录 DSL 版本哈希与提交时校验区间所用的活动索引，执行时据此判断区间是否仍然有效。
func TestStartReplayRecordsHashAndIndex(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.resolver.view.ActiveIndexID = "idx_a"
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z"})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	stored, err := h.service.Store.GetReplay(h.ctx, started.GetReplay().GetReplayId())
	if err != nil || stored.DSLHash != dsl.Hash([]byte(demoDSL)) || stored.ViewIndexID != "idx_a" {
		t.Fatalf("回放应记录版本哈希与活动索引：%+v err=%v", stored, err)
	}
	if started.GetReplay().GetDslHash() != stored.DSLHash {
		t.Fatalf("接口返回的版本哈希不符：%+v", started.GetReplay())
	}
}
