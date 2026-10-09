package rpc

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
)

// 定义锁按引用计数回收：用完即删，不随出现过的定义无限增长；有人等待时不提前删除。
func TestStrategyLocksAreReleased(t *testing.T) {
	h := newHarness(t)
	unlock := h.service.lockStrategy("s1")
	var wg sync.WaitGroup
	wg.Add(1)
	acquired := make(chan struct{})
	go func() {
		defer wg.Done()
		release := h.service.lockStrategy("s1")
		close(acquired)
		release()
	}()
	time.Sleep(20 * time.Millisecond)
	unlock()
	<-acquired
	wg.Wait()
	for i := 0; i < 50; i++ {
		h.service.lockStrategy(fmt.Sprintf("s-%d", i))()
	}
	h.service.locksMu.Lock()
	remaining := len(h.service.locks)
	h.service.locksMu.Unlock()
	if remaining != 0 {
		t.Fatalf("用完的定义锁应被回收：%d", remaining)
	}
}

// 已删除的实例改绑时给出准确提示。
func TestUpdateDeletedInstanceSaysDeleted(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	h.createInstance("i1", "s1", "view_a", "", false)
	deleted, _ := h.service.DeleteStrategyInstance(h.ctx, &strategypb.DeleteStrategyInstanceReq{InstanceId: "i1"})
	requireOK(t, int32(deleted.GetRetInfo().GetCode()), deleted.GetRetInfo().GetMsg())
	rsp, _ := h.service.UpdateStrategyInstance(h.ctx, &strategypb.UpdateStrategyInstanceReq{Instance: &strategypb.StrategyInstance{InstanceId: "i1", StrategyId: "s1", ViewId: "view_b"}})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "实例已删除")
}

// 排队已满时在解析绑定、读取覆盖统计之前就拒绝，不给 Storage 与 Factor 带来负载。
func TestStartReplayChecksActiveLimitFirst(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	for i := 0; i < store.MaxActiveReplays; i++ {
		replay := store.Replay{ReplayID: fmt.Sprintf("p%d", i), DSLYaml: demoDSL, DSLHash: dsl.Hash([]byte(demoDSL)), ViewGeneration: "idx@b1", SpaceID: "crypto", ViewID: "view_a", StartTime: h.now.Add(-48 * time.Hour), EndTime: h.now.Add(-24 * time.Hour), CreatedAt: h.now}
		if err := h.service.Store.CreateReplay(h.ctx, replay); err != nil {
			t.Fatal(err)
		}
	}
	calls := h.resolver.calls
	rsp, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", ViewId: "view_a", StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z"})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "排队或运行中")
	if h.resolver.calls != calls {
		t.Fatalf("排队已满时不应解析绑定：%d → %d", calls, h.resolver.calls)
	}
}
