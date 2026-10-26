package rpc

import (
	"context"
	"errors"
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
	unlock := mustLockStrategy(h.service, "s1")
	var wg sync.WaitGroup
	wg.Add(1)
	acquired := make(chan struct{})
	go func() {
		defer wg.Done()
		release := mustLockStrategy(h.service, "s1")
		close(acquired)
		release()
	}()
	time.Sleep(20 * time.Millisecond)
	unlock()
	<-acquired
	wg.Wait()
	for i := 0; i < 50; i++ {
		mustLockStrategy(h.service, fmt.Sprintf("s-%d", i))()
	}
	h.service.locksMu.Lock()
	remaining := len(h.service.locks)
	h.service.locksMu.Unlock()
	if remaining != 0 {
		t.Fatalf("用完的定义锁应被回收：%d", remaining)
	}
}

// 排队已满时在解析绑定、读取覆盖统计之前就拒绝，不给 Storage 与 Factor 带来负载。
func TestStartReplayChecksActiveLimitFirst(t *testing.T) {
	h := newHarness(t)
	h.createStrategy("s1", demoDSL)
	for i := 0; i < store.MaxActiveReplays; i++ {
		replay := store.Replay{ReplayID: fmt.Sprintf("p%d", i), DSLYaml: demoDSL, DSLHash: dsl.Hash([]byte(demoDSL)), Calendar: "crypto_24x7", ViewGeneration: "idx@b1", SpaceID: "crypto", FactorTaskID: "ftask_a", ViewID: "view_factor_a", StartTime: h.now.Add(-48 * time.Hour), EndTime: h.now.Add(-24 * time.Hour), CreatedAt: h.now}
		if err := h.service.Store.CreateReplay(h.ctx, replay); err != nil {
			t.Fatal(err)
		}
	}
	calls := h.resolver.calls
	rsp, _ := h.service.StartReplay(h.ctx, &strategypb.StartReplayReq{StrategyId: "s1", FactorTaskId: "ftask_a", StartTime: "2026-09-02T00:00:00Z", EndTime: "2026-09-03T00:00:00Z"})
	requireFail(t, int32(rsp.GetRetInfo().GetCode()), rsp.GetRetInfo().GetMsg(), "排队或运行中")
	if h.resolver.calls != calls {
		t.Fatalf("排队已满时不应解析绑定：%d → %d", calls, h.resolver.calls)
	}
}

// 测试辅助：不带期限地取得定义锁（测试里用它模拟后台长时间持锁）。
func mustLockStrategy(s *Service, id string) func() {
	unlock, err := s.lockStrategy(context.Background(), id)
	if err != nil {
		panic(err)
	}
	return unlock
}

// 后台（停用对账）长时间持有定义锁时，带期限的 RPC 必须按期限返回，并回收等待者的引用计数；
// 跨定义的多锁在后续锁等待失败时释放已取得的锁。
func TestStrategyLockWaitHonorsDeadlineAndReleasesReferences(t *testing.T) {
	h := newHarness(t)
	unlock := mustLockStrategy(h.service, "s2") // 末尾显式释放一次（再次调用会永久阻塞）

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := h.service.lockStrategy(ctx, "s2"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockStrategy error = %v, want deadline exceeded", err)
	}
	// 多锁：s1 可得、s2 被占用 → 整体失败，且 s1 已被释放
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err := h.service.lockStrategies(ctx2, "s1", "s2"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockStrategies error = %v, want deadline exceeded", err)
	}
	free, err := h.service.lockStrategy(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	free()
	unlock()
	h.service.locksMu.Lock()
	remaining := len(h.service.locks)
	h.service.locksMu.Unlock()
	if remaining != 0 {
		t.Fatalf("lock table keeps %d entries after all holders and waiters left", remaining)
	}
}
