package rpc

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 改绑等锁期间实例被改绑到第三个定义、且第三个定义正在启用（持有它的锁）：拿锁后重读发现定义变了，
// 换成新定义的锁等启用结束，再按最新状态判断，不能在旧锁下把启用中的实例改绑。
func TestRebindFollowsConcurrentRebind(t *testing.T) {
	for name, operate := range map[string]func(h *harness) (int32, string){
		"改绑": func(h *harness) (int32, string) {
			rsp, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s2"}})
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
			h.createInstance("i1", "s1", "ftask_i1", "", false)
			unlockFirst := mustLockStrategy(h.service, "s1")
			unlockThird := mustLockStrategy(h.service, "s3")
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
			// 另一个请求把实例改绑到 s3，随后在 s3 的锁里把实例启用了。
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
			session := "session-enabled"
			if err := h.service.Store.EnableInstance(h.ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: dsl.Hash([]byte(third)), ResolvedJSON: `{"market":{"select":"spot","prefer":"spot"},"tasks":{"spot":{"dataset_id":"dataset_factor_i1","factor_task_id":"ftask_i1"}}}`, CreatedAt: h.now}, third); err != nil {
				t.Fatal(err)
			}
			unlockThird()
			got := <-done
			requireFail(t, got.code, got.msg, "先停用")
			after, err := h.service.Store.GetInstance(h.ctx, "i1")
			if err != nil || after.StrategyID != "s3" || after.FactorTasks["spot"] != "ftask_i1" || !after.Enabled || after.SessionID == nil || *after.SessionID != session {
				t.Fatalf("启用的实例不应被改绑：%+v err=%v", after, err)
			}
		})
	}
}

// 启用时校验 View 能追溯 min_age_bars：不满足时不写会话、不联系 Trade。
func TestEnableChecksAgeCoverageBeforeSession(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.resolver.coverageErr = errors.New("universe.min_age_bars=240 需要追溯到更早，但 View 当前只覆盖 100 根")
	created := h.createInstance("i1", "s1", "ftask_i1", "acct-1", true)
	requireFail(t, int32(created.GetRetInfo().GetCode()), created.GetRetInfo().GetMsg(), "当前只覆盖")
	sessions, err := h.service.Store.ListSessions(h.ctx, "i1")
	if err != nil || len(sessions) != 0 || len(h.trade.checks) != 0 {
		t.Fatalf("覆盖不足时不应写会话或联系 Trade：sessions=%v checks=%v err=%v", sessions, h.trade.checks, err)
	}
}

// 解析期间实例被并发改绑到另一个定义（越过锁的路径）：启用的事务整体回滚，不留下会话，实例保持停用。
func TestEnableClosesSessionWhenInstanceChangesDuringResolve(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createStrategy("s2", demoDSL+"\n# 第二个定义\n")
	h.createInstance("i1", "s1", "ftask_i1", "acct-1", false)
	h.resolver.onResolve = func() {
		h.resolver.onResolve = nil
		current, err := h.service.Store.GetInstance(h.ctx, "i1")
		if err != nil {
			t.Error(err)
			return
		}
		current.StrategyID, current.UpdatedAt = "s2", h.now
		if err := h.service.Store.UpdateInstance(h.ctx, current); err != nil {
			t.Error(err)
		}
	}
	rsp := h.setEnabled("i1", true)
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "被修改")
	sessions, err := h.service.Store.ListSessions(h.ctx, "i1")
	if err != nil || len(sessions) != 0 {
		t.Fatalf("回滚的启用不应留下会话：%+v err=%v", sessions, err)
	}
	if instance, err := h.service.Store.GetInstance(h.ctx, "i1"); err != nil || instance.SessionID != nil || instance.Enabled {
		t.Fatalf("实例不应挂上会话：%+v err=%v", instance, err)
	}
	// 之后正常重试能启用到改绑后的定义。
	retried := h.setEnabled("i1", true)
	requireOK(t, int32(retried.GetRetInfo().GetCode()), retried.GetRetInfo().GetMsg())
	if retried.GetInstance().GetStrategyId() != "s2" || !retried.GetInstance().GetEnabled() {
		t.Fatalf("重试应启用到改绑后的定义：%+v", retried.GetInstance())
	}
}

// 回放记录 DSL 版本哈希与提交时校验区间所用的活动索引代次，执行时据此判断区间是否仍然有效。
func TestStartReplayRecordsHashAndIndex(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.resolver.view.ActiveIndexID, h.resolver.view.Generation = "idx_a", "idx_a@b7"
	started, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTasks: map[string]string{"spot": "ftask_a"}, StartTime: ms("2026-09-02T00:00:00Z"), EndTime: ms("2026-09-03T00:00:00Z")})
	requireOK(t, int32(started.GetRetInfo().GetCode()), started.GetRetInfo().GetMsg())
	stored, err := h.service.Store.GetReplay(h.ctx, started.GetReplay().GetReplayId())
	if err != nil || stored.DSLHash != dsl.Hash([]byte(demoDSL)) || stored.Tasks["spot"].ViewGeneration != "idx_a@b7" {
		t.Fatalf("回放应记录版本哈希与活动索引：%+v err=%v", stored, err)
	}
	if started.GetReplay().GetDslHash() != stored.DSLHash {
		t.Fatalf("接口返回的版本哈希不符：%+v", started.GetReplay())
	}
}
