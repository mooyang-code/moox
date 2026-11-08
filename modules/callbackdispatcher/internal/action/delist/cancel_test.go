package delist

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

func (h *harness) execution(id string) store.Execution {
	h.t.Helper()
	execution, err := h.store.GetExecution(context.Background(), id)
	require.NoError(h.t, err)
	return execution
}

// cancel 按服务层的顺序取消：先原子置为已取消，再善后（传入没有截止时间的 ctx）。
func (h *harness) cancel(id string) error {
	h.t.Helper()
	ctx := context.WithoutCancel(context.Background())
	before, err := h.store.CancelExecution(ctx, id, h.clock.Now().UnixMilli())
	require.NoError(h.t, err)
	return h.handler.OnCancel(ctx, before)
}

// 取消解除禁止；重复取消（善后重试）幂等。
func TestOnCancelUnblocksAndIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.step("e1", baseTime)
	require.True(t, h.trade.isBlocked())
	require.NoError(t, h.cancel("e1"))
	require.False(t, h.trade.isBlocked())
	require.NoError(t, h.handler.OnCancel(context.Background(), h.execution("e1")))
	require.False(t, h.trade.isBlocked())
}

// 演练（PAPER_ONLY）取消时，同标的的真实下架还没结束：不解除禁止。
func TestDrillCancelKeepsRealBlock(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.addExecution("real", delistAt, "ALL")
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("real", baseTime)
	h.step("drill", baseTime)
	require.NoError(t, h.cancel("drill"))
	require.True(t, h.trade.isBlocked(), "真实下架还靠这个禁止")
	require.Zero(t, h.trade.callCount("Unblock"))
}

// 真实记录取消时一律解除名单（不管同标的演练有没有结束）：Trade 只在解除禁止时才停下清仓，不解除的话已发起的平仓会继续卖。
// 演练下一次推进前查名单发现不在了，会自己停下。
func TestRealCancelAlwaysUnblocksAndDrillStopsItself(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.addExecution("real", delistAt, "ALL")
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("real", baseTime)
	h.step("drill", baseTime)

	require.NoError(t, h.cancel("real"))
	require.False(t, h.trade.isBlocked())
	outcome := h.step("drill", baseTime.Add(time.Hour))
	require.Equal(t, store.StatusCanceled, outcome.Status)
	require.Contains(t, outcome.Error, "手工解除")
}

// 取消发生在 Step 写名单期间：名单是在取消之后才写进去的，Step 复查后补一次解除。
func TestCancelDuringBlockUnblocksAfterwards(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	entered, proceed := make(chan struct{}), make(chan struct{})
	h.trade.blockHook = func(context.Context) {
		close(entered)
		<-proceed
	}
	h.clock.Set(baseTime)

	var wg sync.WaitGroup
	var outcome action.Outcome
	wg.Add(1)
	go func() {
		defer wg.Done()
		outcome, _ = h.handler.Step(ctx, h.execution("e1"), baseTime)
	}()
	<-entered
	// 写名单还没返回时用户取消：先置为已取消，再善后；善后要等标的锁（Step 还握着）。
	before, err := h.store.CancelExecution(ctx, "e1", baseTime.UnixMilli())
	require.NoError(t, err)
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- h.handler.OnCancel(context.WithoutCancel(ctx), before) }()
	select {
	case <-cancelDone:
		t.Fatal("OnCancel 应当等 Step 写名单这一段结束")
	case <-time.After(50 * time.Millisecond):
	}
	close(proceed)
	wg.Wait()
	require.NoError(t, <-cancelDone)
	require.Equal(t, store.StatusCanceled, outcome.Status)
	require.False(t, h.trade.isBlocked(), "取消之后写进去的名单被解除")
	require.Zero(t, h.trade.flattenCount())
}

// 取消发生在 Step 写名单期间，补解除又失败：记录不是执行中，要把“善后未完成”标记写回库。
func TestCancelDuringBlockCleanupFailureIsRecorded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.trade.blockHook = func(context.Context) {
		_, err := h.store.CancelExecution(ctx, "e1", baseTime.UnixMilli())
		require.NoError(t, err)
	}
	h.trade.unblockHook = func(context.Context) error { return errors.New("Trade 不可达") }
	h.clock.Set(baseTime)
	outcome, err := h.handler.Step(ctx, h.execution("e1"), baseTime)
	require.NoError(t, err)
	require.Equal(t, store.StatusCanceled, outcome.Status)
	got := h.execution("e1")
	require.Equal(t, store.StatusCanceled, got.Status)
	require.True(t, got.CleanupPending, "用户能看到“重试善后”，关注项也会提醒")
	require.Contains(t, got.LastError, "取消后的善后失败")
	pending, err := h.store.ListPendingCleanups(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// 之后善后恢复，再取消一次就做成。
	h.trade.unblockHook = nil
	require.NoError(t, h.handler.OnCancel(ctx, got))
	require.False(t, h.trade.isBlocked())
}

// 演练取消与同标的真实下架写名单并发：演练的善后要等真实那条的“写名单 + 复查”结束，之后看到真实还在，就不解除。
func TestDrillCancelRacingRealBlockKeepsList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	delistAt := baseTime.Add(48 * time.Hour)
	h.addExecution("real", delistAt, "ALL")
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("drill", baseTime)
	require.True(t, h.trade.isBlocked())

	entered, proceed := make(chan struct{}), make(chan struct{})
	h.trade.blockHook = func(context.Context) {
		close(entered)
		<-proceed
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = h.handler.Step(ctx, h.execution("real"), baseTime)
	}()
	<-entered
	before, err := h.store.CancelExecution(ctx, "drill", baseTime.UnixMilli())
	require.NoError(t, err)
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- h.handler.OnCancel(context.WithoutCancel(ctx), before) }()
	time.Sleep(30 * time.Millisecond)
	close(proceed)
	wg.Wait()
	require.NoError(t, <-cancelDone)
	require.True(t, h.trade.isBlocked(), "真实下架刚写进名单，演练的取消不能把它解除")
	require.Zero(t, h.trade.callCount("Unblock"))
}

// 快速连点两次取消：两路 OnCancel 同时执行，结果仍然正确。
func TestConcurrentOnCancel(t *testing.T) {
	h := newHarness(t)
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.step("e1", baseTime)
	before, err := h.store.CancelExecution(context.Background(), "e1", baseTime.UnixMilli())
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- h.handler.OnCancel(context.WithoutCancel(context.Background()), before)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.False(t, h.trade.isBlocked())
}

// OnCancel 的 ctx 没有截止时间：Trade 卡住时处理器自己加时限，取消请求不会一直挂着。
func TestOnCancelAppliesOwnTradeTimeout(t *testing.T) {
	h := newHarness(t)
	h.handler.TradeTimeout = 50 * time.Millisecond
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.step("e1", baseTime)
	h.trade.unblockHook = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	_, err := h.store.CancelExecution(context.Background(), "e1", baseTime.UnixMilli())
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- h.handler.OnCancel(context.WithoutCancel(context.Background()), h.execution("e1")) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		t.Fatal("OnCancel 没有加时限，Trade 卡住时一直挂着")
	}
}

// 卡住后被放回的旧线程和新线程同时推进同一步：用的是同一个操作号，Trade 只平一次。
func TestStaleAndFreshThreadsUseSameActionID(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"), live("crypto", "la-2"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	at := delistAt.Add(-24 * time.Hour)
	h.clock.Set(at)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := h.handler.Step(context.Background(), h.execution("e1"), at)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, 2, h.trade.flattenCount(), "每个组合只平一次")
	h.trade.mu.Lock()
	defer h.trade.mu.Unlock()
	seen := map[string]bool{}
	for _, request := range h.trade.flattens {
		require.False(t, seen[request.ActionID])
		seen[request.ActionID] = true
		require.Equal(t, operatorActionID("e1", RoundFlatten, request.SpaceID, request.LogicalAccountID, delistAt.UnixMilli(), 0), request.ActionID)
		require.Equal(t, flattenReason(h.execution("e1"), target{Instrument: foo}, RoundFlatten), request.Reason)
	}
}

// 同一标的的锁互相不挡别的标的。
func TestKeyedMutexIsPerKey(t *testing.T) {
	var locks keyedMutex
	unlockA := locks.Lock("a")
	done := make(chan struct{})
	go func() {
		unlockB := locks.Lock("b")
		unlockB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("不同标的的锁不应互相等待")
	}
	unlockA()
	require.Empty(t, locks.locks, "没人用的键会被清掉")
}
