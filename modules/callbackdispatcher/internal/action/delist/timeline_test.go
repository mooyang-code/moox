package delist

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/dispatch"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// 经工作线程和模拟时钟走完整条时间线：发现即禁止 → 等到 T−24 小时平仓 → T−2 小时复查 → T 后核对。
func TestTimelineThroughWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"), live("crypto", "la-1"), live("crypto", "la-2"), paper("paper-space", "la-p"))
	h.addExecution("e1", delistAt, "ALL")

	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, h.handler)
	worker := &dispatch.Worker{Store: h.store, Actions: registry, Batch: 4, PollInterval: time.Second, RetryWindow: time.Hour, RetentionDays: 30, Now: h.clock.Now}
	runAt := func(at time.Time) store.Execution {
		h.clock.Set(at)
		worker.RunOnce(ctx)
		execution, err := h.store.GetExecution(ctx, "e1")
		require.NoError(t, err)
		return execution
	}

	// 发现：立刻禁止买入，并因为有持有者产生 delist_detected；下一次推进是 T−24 小时。
	execution := runAt(baseTime)
	require.Equal(t, store.StatusPending, execution.Status)
	require.Equal(t, delistAt.Add(-24*time.Hour).UnixMilli(), execution.NextRunAt)
	require.True(t, h.trade.isBlocked())
	require.Zero(t, h.trade.flattenCount(), "还没到 T−24 小时不平仓")
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[blockKey()].Status)
	require.NotEmpty(t, telemetry.Recent(telemetry.AttentionDelistDetected, baseTime, time.Minute))

	// 等待期间每次推进前都查名单，不下单。
	execution = runAt(baseTime.Add(23 * time.Hour))
	require.Equal(t, store.StatusPending, execution.Status)
	require.Zero(t, h.trade.flattenCount())

	// T−24 小时：持有者每个执行账户一行，按（空间、组合）聚合成三个组合，各平一次。
	execution = runAt(delistAt.Add(-24 * time.Hour))
	require.Equal(t, 3, h.trade.flattenCount())
	require.Equal(t, delistAt.Add(-24*time.Hour+30*time.Second).UnixMilli(), execution.NextRunAt, "平仓中每 30 秒查一次")
	steps := h.steps("e1")
	require.Equal(t, store.DelistRunning, steps[flattenKey(RoundFlatten, "crypto", "la-1")].Status)
	require.Equal(t, store.DelistRunning, steps[summaryKey(RoundFlatten)].Status)

	execution = runAt(delistAt.Add(-24*time.Hour + 30*time.Second))
	steps = h.steps("e1")
	for _, account := range []account{{"crypto", "la-1"}, {"crypto", "la-2"}, {"paper-space", "la-p"}} {
		require.Equal(t, store.DelistSucceeded, steps[flattenKey(RoundFlatten, account.SpaceID, account.LogicalAccountID)].Status, account)
	}
	require.Equal(t, store.DelistSucceeded, steps[summaryKey(RoundFlatten)].Status)
	require.Equal(t, delistAt.Add(-2*time.Hour).UnixMilli(), execution.NextRunAt, "下一次是 T−2 小时复查")

	// T−2 小时：复查，已经没有持有者，不再下单。
	execution = runAt(delistAt.Add(-2 * time.Hour))
	require.Equal(t, 3, h.trade.flattenCount())
	steps = h.steps("e1")
	require.Equal(t, store.DelistSucceeded, steps[summaryKey(RoundReview)].Status)
	require.Contains(t, steps[summaryKey(RoundReview)].Result, "没有策略持有")
	require.Equal(t, delistAt.UnixMilli(), execution.NextRunAt)

	// T：停止平仓，等 10 分钟后核对。
	execution = runAt(delistAt)
	require.Equal(t, store.StatusPending, execution.Status)
	require.Equal(t, delistAt.Add(10*time.Minute).UnixMilli(), execution.NextRunAt)

	execution = runAt(delistAt.Add(10 * time.Minute))
	require.Equal(t, store.StatusSucceeded, execution.Status)
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[verifyKey()].Status)
	require.True(t, h.trade.isBlocked(), "下架后名单保留")
}

// 第 2 轮复查：第 1 轮 PARTIAL 的组合换新的操作号再平，并且保留 PARTIAL 的原因。
func TestPartialIsReviewedWithNewActionID(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	var attempts int
	h.trade.finalFor = func(action.FlattenRequest) (string, string) {
		attempts++
		if attempts == 1 {
			return action.OperatorPartial, "最近一笔被交易所拒绝：最小下单额"
		}
		return action.OperatorCompleted, ""
	}

	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))
	h.step("e1", delistAt.Add(-24*time.Hour+30*time.Second))
	first := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistPartial, first.Status)
	require.Contains(t, first.Error, "最近一笔被交易所拒绝")
	require.Equal(t, store.DelistPartial, h.steps("e1")[summaryKey(RoundFlatten)].Status)

	// 之后的推进里 PARTIAL 的行不会同号重试。
	h.step("e1", delistAt.Add(-23*time.Hour))
	require.Equal(t, 1, h.trade.flattenCount())

	// T−2 小时复查：新的操作号。
	h.step("e1", delistAt.Add(-2*time.Hour))
	h.step("e1", delistAt.Add(-2*time.Hour+30*time.Second))
	second := h.steps("e1")[flattenKey(RoundReview, "crypto", "la-1")]
	require.Equal(t, store.DelistSucceeded, second.Status)
	require.NotEqual(t, first.OperatorActionID, second.OperatorActionID)
	require.Equal(t, 2, h.trade.flattenCount())

	// 核对通过。
	outcome := h.step("e1", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusSucceeded, outcome.Status)
}

// 过了下架时间收到事件（手工补录）：照样写名单，然后直接核对，不下单。
func TestDelistTimeAlreadyPassedOnlyVerifies(t *testing.T) {
	h := newHarness(t)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", baseTime.Add(-time.Hour), "ALL")

	outcome := h.step("e1", baseTime)
	require.Equal(t, store.StatusFailed, outcome.Status, "核对时仍有持有")
	require.Contains(t, outcome.Error, "crypto/la-1")
	require.True(t, h.trade.isBlocked())
	require.Zero(t, h.trade.flattenCount(), "过了下架时间不下单")
	steps := h.steps("e1")
	require.Equal(t, store.DelistFailed, steps[verifyKey()].Status)
	require.NotContains(t, steps, summaryKey(RoundFlatten))

	// 持有已清掉，重新执行后核对通过。
	h.trade.setHolders()
	require.Equal(t, store.StatusSucceeded, h.step("e1", baseTime.Add(time.Minute)).Status)
}

// 下架时间刚过不久：先等 10 分钟再核对（交易所结算后 Trade 要同步才更新）。
func TestVerifyWaitsAfterDelistTime(t *testing.T) {
	h := newHarness(t)
	h.addExecution("e1", baseTime.Add(-time.Minute), "ALL")
	outcome := h.step("e1", baseTime)
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Equal(t, baseTime.Add(9*time.Minute), outcome.NextRunAt)
}

// 中途改下架时间：下一次推进按新的时间重新规划。
func TestDelistTimeChangedReplans(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", baseTime.Add(72*time.Hour), "ALL")
	outcome := h.step("e1", baseTime)
	require.Equal(t, baseTime.Add(48*time.Hour), outcome.NextRunAt)

	replace := func(delistAt time.Time) {
		require.NoError(t, h.store.InTx(ctx, func(tx *store.Tx) error {
			return tx.ReplaceExecutionEvent("e1", "event-2", payloadAt(delistAt, "ALL"), baseTime.UnixMilli(), baseTime.UnixMilli())
		}))
	}
	// 推后：平仓时间跟着后移。
	replace(baseTime.Add(120 * time.Hour))
	outcome = h.step("e1", baseTime.Add(time.Hour))
	require.Equal(t, baseTime.Add(96*time.Hour), outcome.NextRunAt)
	require.Zero(t, h.trade.flattenCount())

	// 提前到 10 小时后：已经过了 T−24 小时，立刻平仓；复查在 T−2 小时。
	replace(baseTime.Add(10 * time.Hour))
	outcome = h.step("e1", baseTime.Add(2*time.Hour))
	require.Equal(t, 1, h.trade.flattenCount())
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Equal(t, baseTime.Add(2*time.Hour+30*time.Second), outcome.NextRunAt)
	outcome = h.step("e1", baseTime.Add(2*time.Hour+30*time.Second))
	require.Equal(t, baseTime.Add(8*time.Hour), outcome.NextRunAt, "复查改按新时间的 T−2 小时")
	require.Equal(t, 1, h.trade.flattenCount(), "已平的不会因为改期重复平")
}

// 重复执行同一步不重复平仓：已存的行、操作号不变，已完成的不再调用。
func TestRepeatedStepDoesNotFlattenTwice(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"), live("crypto", "la-2"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)

	at := delistAt.Add(-24 * time.Hour)
	h.step("e1", at)
	ids := map[string]string{}
	for key, step := range h.steps("e1") {
		if key.LogicalAccountID != "" {
			ids[key.LogicalAccountID] = step.OperatorActionID
		}
	}
	require.Len(t, ids, 2)
	// 同一时刻又推进一次（进程重启、被放回）：同号再调，Trade 幂等，不新增清仓。
	h.step("e1", at)
	require.Equal(t, 2, h.trade.flattenCount())
	for key, step := range h.steps("e1") {
		if key.LogicalAccountID != "" {
			require.Equal(t, ids[key.LogicalAccountID], step.OperatorActionID)
		}
	}
	h.step("e1", at.Add(30*time.Second))
	callsBefore := h.trade.callCount("Flatten")
	h.step("e1", at.Add(time.Minute))
	require.Equal(t, callsBefore, h.trade.callCount("Flatten"), "已完成的不再发起")
	require.Equal(t, 2, h.trade.flattenCount())
}

// 过了下架时间不下单：Step 开始时还没到，但发起平仓之前已经过了，也不下单。
func TestNoOrderAfterDelistTimeEvenMidStep(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(30 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)

	// 处理器看到的 now 在 T 之前，但真实时钟已经过了 T。
	h.clock.Set(delistAt.Add(time.Second))
	execution, err := h.store.GetExecution(context.Background(), "e1")
	require.NoError(t, err)
	_, err = h.handler.Step(context.Background(), execution, delistAt.Add(-time.Minute))
	require.NoError(t, err)
	require.Zero(t, h.trade.flattenCount())
	step := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistFailed, step.Status)
	require.Contains(t, step.Error, "已到下架时间")
}

// 到了下架时间，没结束的平仓一律停下（不再重试）。
func TestRetriesStopAtDelistTime(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(30 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorFailed, "交易所拒单" }
	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))
	h.step("e1", delistAt.Add(-24*time.Hour+30*time.Second))
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistPending, row.Status)
	require.Equal(t, 1, row.Attempts)

	outcome := h.step("e1", delistAt)
	require.Equal(t, store.StatusPending, outcome.Status)
	row = h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistFailed, row.Status)
	require.Contains(t, row.Error, "已到下架时间，停止平仓")
	require.Equal(t, store.DelistFailed, h.steps("e1")[summaryKey(RoundFlatten)].Status)
}

// 解除禁止后停止：名单里没有了，记录置为已取消，不再下单。
func TestManualUnblockStopsExecution(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)

	h.trade.setBlocked(false)
	outcome := h.step("e1", delistAt.Add(-24*time.Hour))
	require.Equal(t, store.StatusCanceled, outcome.Status)
	require.Contains(t, outcome.Error, "手工解除")
	require.Zero(t, h.trade.flattenCount())
}

// Trade 在清仓时发现已解除禁止而提前结束：不算失败，不重试。
func TestUnblockedDuringFlattenIsNotFailure(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))

	h.handler.Trade = unblockedTrade{h.trade}
	h.step("e1", delistAt.Add(-24*time.Hour+30*time.Second))
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistSucceeded, row.Status)
	require.Equal(t, 0, row.Attempts)
	require.Contains(t, row.Result, "解除禁止")
}

type unblockedTrade struct{ *fakeTrade }

func (u unblockedTrade) GetOperatorAction(ctx context.Context, spaceID, id string) (action.OperatorAction, error) {
	result, err := u.fakeTrade.GetOperatorAction(ctx, spaceID, id)
	result.Unblocked = true
	return result, err
}
