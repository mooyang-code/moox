package delist

// 总审查的回归用例。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/dispatch"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

// 中 1：调用 Trade 的暂时失败都放进这次推进的问题说明（工作线程据此累计连续失败次数），包括查平仓结果出错；恢复后说明为空。
func TestTransientTradeErrorsAreReportedInOutcome(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	at := flattenAt(h, "e1", delistAt)
	require.Equal(t, store.DelistRunning, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)

	h.trade.setErr("GetAction", errors.New("Trade 不可达"))
	at = at.Add(30 * time.Second)
	outcome := h.step("e1", at)
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Contains(t, outcome.Error, "查询平仓结果失败")
	require.Equal(t, at.Add(30*time.Second), outcome.NextRunAt)
	require.Zero(t, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Attempts, "查询出错不计这一行的重试次数")

	h.trade.setErr("GetAction", nil)
	outcome = h.step("e1", at.Add(30*time.Second))
	require.Empty(t, outcome.Error)
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)

	// 核对时查持有者出错同样带说明。
	h.trade.setErr("ListHolders", errors.New("Trade 不可达"))
	outcome = h.step("e1", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Contains(t, outcome.Error, "核对时查持有者失败")
}

// throughWorker 让工作线程按模拟时钟推进记录，返回推进后的记录：结果经 SaveResult 写回，处理期间合并进来的新事件会把记录改回待处理。
func (h *harness) throughWorker() func(id string, at time.Time) store.Execution {
	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, h.handler)
	worker := &dispatch.Worker{Store: h.store, Actions: registry, Batch: 4, PollInterval: time.Second, RetryWindow: time.Hour, RetentionDays: 30, Now: h.clock.Now}
	return func(id string, at time.Time) store.Execution {
		h.t.Helper()
		h.clock.Set(at)
		worker.RunOnce(context.Background())
		execution, err := h.store.GetExecution(context.Background(), id)
		require.NoError(h.t, err)
		return execution
	}
}

// merge 模拟同标的又来一次演练登记、下架时间不同：事件接收把新内容合并进这条未结束的记录。
func (h *harness) merge(id string, delistAt time.Time) {
	h.t.Helper()
	require.NoError(h.t, h.store.InTx(context.Background(), func(tx *store.Tx) error {
		return tx.ReplaceExecutionEvent(id, "event-merged-"+id, payloadAt(delistAt, "PAPER_ONLY"), h.clock.Now().UnixMilli(), h.clock.Now().UnixMilli())
	}))
}

// 小 1 场景 1：演练最后一步（核对 + 解除）进行中，同标的又来一次演练登记、下架时间不同，SaveResult 把记录改回待处理。
// 下一次推进发现名单里没有这一项，但这是自己结束时解除的：重新写名单，按新的下架时间重走时间线（已成功的轮次也重开），不置为已取消。
func TestDrillReopenedDuringFinalStepRestartsTimeline(t *testing.T) {
	h := newHarness(t)
	run := h.throughWorker()
	delistAt := baseTime.Add(3 * time.Hour)
	h.trade.setHolders(paper("crypto", "la-p"))
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	run("drill", baseTime) // 写名单，第 1 轮立刻发起
	run("drill", baseTime.Add(31*time.Second))
	run("drill", delistAt.Add(-2*time.Hour))
	require.Equal(t, 1, h.trade.flattenCount())
	require.Equal(t, store.DelistSucceeded, h.steps("drill")[summaryKey(RoundFlatten)].Status)

	newDelistAt := delistAt.Add(48 * time.Hour)
	var once sync.Once
	h.trade.unblockHook = func(context.Context) error {
		once.Do(func() { h.merge("drill", newDelistAt) })
		return nil
	}
	execution := run("drill", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusPending, execution.Status, "处理期间合并了新登记：结果不写，记录改回待处理")
	require.False(t, h.trade.isBlocked(), "结束时已自己解除")
	require.True(t, h.steps("drill")[blockKey()].SelfReleased)

	blocks := h.trade.callCount("Block")
	execution = run("drill", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusPending, execution.Status, "不能当作手工解除而置为已取消")
	require.True(t, h.trade.isBlocked(), "重新写名单")
	require.Equal(t, blocks+1, h.trade.callCount("Block"))
	block := h.steps("drill")[blockKey()]
	require.False(t, block.SelfReleased)
	require.Equal(t, newDelistAt.UnixMilli(), block.PlannedDelistAt)
	require.Equal(t, newDelistAt.Add(-24*time.Hour).UnixMilli(), execution.NextRunAt, "按新的下架时间走")

	// 新时间线：已成功的第 1 轮按新时间重开，新出现的模拟盘持仓照常平掉；结束时再次解除。
	h.trade.setHolders(paper("crypto", "la-p"))
	run("drill", newDelistAt.Add(-24*time.Hour))
	require.Equal(t, 2, h.trade.flattenCount())
	run("drill", newDelistAt.Add(-24*time.Hour+31*time.Second))
	run("drill", newDelistAt.Add(-2*time.Hour))
	execution = run("drill", newDelistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusSucceeded, execution.Status)
	require.False(t, h.trade.isBlocked())
}

// 重新打开之后名单里还有这一项时清掉“自己解除过”：之后再不见了就是被手工解除的，照常置为已取消。
func TestDrillSelfReleasedFlagClearsWhileListed(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("drill", baseTime)
	block := h.steps("drill")[blockKey()]
	block.SelfReleased = true
	require.NoError(t, h.store.UpsertDelistAction(context.Background(), block))

	require.Equal(t, store.StatusPending, h.step("drill", baseTime.Add(time.Hour)).Status)
	require.False(t, h.steps("drill")[blockKey()].SelfReleased)
	h.trade.setBlocked(false)
	out := h.step("drill", baseTime.Add(2*time.Hour))
	require.Equal(t, store.StatusCanceled, out.Status)
	require.Contains(t, out.Error, "手工解除")
}

// 小 1 场景 2：演练核对失败（结束时已自己解除名单）后点“重新执行”：从写名单开始重走，不会立即变成“禁止买入已被手工解除”。
func TestFailedDrillRetryReblocksAndReverifies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	run := h.throughWorker()
	delistAt := baseTime.Add(3 * time.Hour)
	h.trade.setHolders(paper("crypto", "la-p"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorPartial, "拒单" }
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	run("drill", baseTime)
	run("drill", baseTime.Add(31*time.Second))
	execution := run("drill", delistAt.Add(11*time.Minute))
	require.Equal(t, store.StatusFailed, execution.Status)
	require.False(t, h.trade.isBlocked())

	_, err := h.store.RetryExecution(ctx, "drill", delistAt.Add(20*time.Minute).UnixMilli())
	require.NoError(t, err)
	block := h.steps("drill")[blockKey()]
	require.Equal(t, store.DelistPending, block.Status, "重新执行重置禁止买入那一行")
	require.False(t, block.SelfReleased)
	require.Empty(t, block.ReleaseVerdict)

	// 持仓已人工处理：重新写名单、重新核对后成功，结束时再次解除。
	h.trade.setHolders()
	blocks := h.trade.callCount("Block")
	execution = run("drill", delistAt.Add(20*time.Minute))
	require.Equal(t, store.StatusSucceeded, execution.Status)
	require.Equal(t, blocks+1, h.trade.callCount("Block"))
	require.Equal(t, 2, h.steps("drill")[verifyKey()].Attempts, "重新核对了一次")
	require.False(t, h.trade.isBlocked())
}

// 重新执行只重置演练记录的禁止买入那一行：真实下架的名单按设计保留，那一行不动。
func TestRetryKeepsRealDelistBlockStep(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	run := h.throughWorker()
	delistAt := baseTime.Add(3 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorPartial, "拒单" }
	h.addExecution("real", delistAt, "ALL")
	run("real", baseTime)
	run("real", baseTime.Add(31*time.Second))
	require.Equal(t, store.StatusFailed, run("real", delistAt.Add(11*time.Minute)).Status)
	_, err := h.store.RetryExecution(ctx, "real", delistAt.Add(20*time.Minute).UnixMilli())
	require.NoError(t, err)
	require.Equal(t, store.DelistSucceeded, h.steps("real")[blockKey()].Status)
}

// 小 1 场景 3：演练只剩重试解除（结论已存下），这时同标的又来一次演练登记、下架时间不同：旧结论作废，重新写名单、按新时间线走，
// 即使解除其实已经生效（名单里没有这一项），也不会返回旧结论或置为已取消。
func TestDrillReleaseRetryWithMergedScheduleRestarts(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(3 * time.Hour)
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("drill", baseTime)
	lost := true
	h.trade.unblockHook = func(context.Context) error {
		if lost {
			// 解除已在 Trade 上生效，响应丢了。
			lost = false
			h.trade.mu.Lock()
			delete(h.trade.blocked, foo)
			h.trade.mu.Unlock()
			return context.DeadlineExceeded
		}
		return nil
	}
	out := h.step("drill", delistAt.Add(11*time.Minute))
	require.Equal(t, store.StatusPending, out.Status)
	block := h.steps("drill")[blockKey()]
	require.Equal(t, store.StatusSucceeded, block.ReleaseVerdict)
	require.Equal(t, delistAt.UnixMilli(), block.PlannedDelistAt, "结论记着它对应的下架时间")

	newDelistAt := delistAt.Add(48 * time.Hour)
	h.clock.Set(delistAt.Add(12 * time.Minute))
	h.merge("drill", newDelistAt)
	blocks := h.trade.callCount("Block")
	out = h.step("drill", delistAt.Add(12*time.Minute))
	require.Equal(t, store.StatusPending, out.Status, "不返回按旧下架时间得出的结论")
	require.Equal(t, newDelistAt.Add(-24*time.Hour), out.NextRunAt)
	require.Equal(t, blocks+1, h.trade.callCount("Block"))
	require.True(t, h.trade.isBlocked())
	block = h.steps("drill")[blockKey()]
	require.Empty(t, block.ReleaseVerdict)
	require.Equal(t, newDelistAt.UnixMilli(), block.PlannedDelistAt)

	require.Equal(t, store.StatusSucceeded, h.step("drill", newDelistAt.Add(10*time.Minute)).Status)
	require.False(t, h.trade.isBlocked())
}
