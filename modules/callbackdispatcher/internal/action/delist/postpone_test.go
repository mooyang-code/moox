package delist

// 第二轮总审查的回归用例：改期后进行中的平仓轮次、Trade 一直正忙、Trade 拒绝参数、演练重开。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

// driveUntil 按每次返回的 NextRunAt 推进，直到记录结束，或下一次推进的时间晚于 until；返回最后一次的结果。
func driveUntil(t *testing.T, h *harness, id string, from, until time.Time) action.Outcome {
	t.Helper()
	at := from
	for i := 0; i < 10000; i++ {
		outcome := h.step(id, at)
		if outcome.Status != store.StatusPending || outcome.NextRunAt.After(until) {
			return outcome
		}
		at = outcome.NextRunAt
	}
	t.Fatal("推进次数超过上限")
	return action.Outcome{}
}

// reschedule 把记录的下架时间合并改成 delistAt（事件接收合并同标的新事件的结果）。
func (h *harness) reschedule(id string, delistAt time.Time, scope string, at time.Time) {
	h.t.Helper()
	h.clock.Set(at)
	require.NoError(h.t, h.store.InTx(context.Background(), func(tx *store.Tx) error {
		return tx.ReplaceExecutionEvent(id, "event-resched-"+id, payloadAt(delistAt, scope), at.UnixMilli(), at.UnixMilli())
	}))
}

// requireLaunchedBeforeDeadline 断言每次发起清仓时截止时间都还没到：不会用已经过去的截止时间发起，让 Trade 立刻记 PARTIAL。
func requireLaunchedBeforeDeadline(t *testing.T, trade *fakeTrade) {
	t.Helper()
	trade.mu.Lock()
	defer trade.mu.Unlock()
	require.Len(t, trade.launchedAt, len(trade.flattens))
	for i, request := range trade.flattens {
		require.True(t, trade.launchedAt[i].Before(time.UnixMilli(request.DeadlineAt)), "第 %d 次发起时截止时间已过：发起 %s，截止 %s",
			i+1, trade.launchedAt[i].UTC().Format(time.RFC3339), time.UnixMilli(request.DeadlineAt).UTC().Format(time.RFC3339))
	}
}

func (h *harness) failedSteps() []store.Execution {
	h.t.Helper()
	failed, err := h.store.ListUnfinishedWithFailedSteps(context.Background())
	require.NoError(h.t, err)
	return failed
}

// untilDeadline 模拟 Trade 的截止时间：截止时间没到一直在跑，到了就停止下单、记 PARTIAL。
func untilDeadline(h *harness) func(action.FlattenRequest) (string, string) {
	return func(request action.FlattenRequest) (string, string) {
		if h.clock.Now().Before(time.UnixMilli(request.DeadlineAt)) {
			return action.OperatorRunning, ""
		}
		return action.OperatorPartial, "已到截止时间，停止下单"
	}
}

// 中 1 场景 A：第 1 轮在旧计划下进行中时推迟 3 天。旧行继续查结果直到 Trade 在旧截止时间停下（PARTIAL），
// 到新的 T−24h 第 1 轮按新时间重开，用新的截止时间发起，不是等到新的 T−2h 才重开。
func TestPostponedWhileFirstRoundRunning(t *testing.T) {
	h := newHarness(t)
	oldT := baseTime.Add(30 * time.Hour)
	newT := oldT.Add(72 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	running := untilDeadline(h)
	h.trade.finalFor = func(request action.FlattenRequest) (string, string) {
		if request.DeadlineAt == oldT.UnixMilli() {
			return running(request)
		}
		return action.OperatorCompleted, ""
	}
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.step("e1", oldT.Add(-24*time.Hour))
	require.Equal(t, store.DelistRunning, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)

	mergeAt := oldT.Add(-23 * time.Hour)
	h.reschedule("e1", newT, "ALL", mergeAt)
	polls := h.trade.callCount("GetAction")
	outcome := driveUntil(t, h, "e1", mergeAt, oldT.Add(time.Minute))
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistPartial, row.Status, "旧行一直查到 Trade 在旧截止时间停下")
	require.Equal(t, oldT.UnixMilli(), row.PlannedDelistAt)
	require.Greater(t, h.trade.callCount("GetAction"), polls+40, "改期后旧行照常每 30 秒查一次")
	require.Equal(t, newT.Add(-24*time.Hour), outcome.NextRunAt, "下一次在新的 T−24h")

	outcome = driveUntil(t, h, "e1", outcome.NextRunAt, newT.Add(20*time.Minute))
	require.Equal(t, store.StatusSucceeded, outcome.Status, outcome.Error)
	require.Len(t, h.trade.flattens, 2)
	require.Equal(t, newT.UnixMilli(), h.trade.flattens[1].DeadlineAt)
	require.Equal(t, newT.Add(-24*time.Hour), h.trade.launchedAt[1], "第 1 轮在新的 T−24h 重开")
	requireLaunchedBeforeDeadline(t, h.trade)
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[verifyKey()].Status)
}

// 中 1 场景 B：第 1 轮已成功，又查到残留；第 2 轮在旧计划下进行中时推迟 24 小时，Trade 按旧截止时间记 PARTIAL。
// 第 2 轮到新的 T−2h 按新时间重开再平一次，新 T 之后核对通过。
func TestPostponedWhileReviewRoundRunning(t *testing.T) {
	h := newHarness(t)
	oldT := baseTime.Add(30 * time.Hour)
	newT := oldT.Add(24 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.step("e1", oldT.Add(-24*time.Hour))
	h.step("e1", oldT.Add(-24*time.Hour+31*time.Second))
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[summaryKey(RoundFlatten)].Status)

	h.trade.setHolders(live("crypto", "la-1")) // 又查到残留
	running := untilDeadline(h)
	h.trade.finalFor = func(request action.FlattenRequest) (string, string) {
		if request.DeadlineAt == oldT.UnixMilli() {
			return running(request)
		}
		return action.OperatorCompleted, ""
	}
	h.step("e1", oldT.Add(-2*time.Hour))
	require.Equal(t, store.DelistRunning, h.steps("e1")[flattenKey(RoundReview, "crypto", "la-1")].Status)

	mergeAt := oldT.Add(-2*time.Hour + 10*time.Second)
	h.reschedule("e1", newT, "ALL", mergeAt)
	outcome := driveUntil(t, h, "e1", mergeAt, oldT.Add(time.Minute))
	require.Equal(t, store.DelistPartial, h.steps("e1")[flattenKey(RoundReview, "crypto", "la-1")].Status)
	require.Equal(t, store.DelistPartial, h.steps("e1")[summaryKey(RoundReview)].Status)
	require.Equal(t, newT.Add(-2*time.Hour), outcome.NextRunAt, "第 1 轮已成功不重开，第 2 轮在新的 T−2h 重开")

	outcome = driveUntil(t, h, "e1", outcome.NextRunAt, newT.Add(-2*time.Hour+time.Second))
	require.Empty(t, h.failedSteps(), "重开后旧计划的 PARTIAL 行已被新行取代，不再触发关注项")
	outcome = driveUntil(t, h, "e1", outcome.NextRunAt, newT.Add(20*time.Minute))
	require.Equal(t, store.StatusSucceeded, outcome.Status, outcome.Error)
	require.Len(t, h.trade.flattens, 3)
	require.Equal(t, newT.UnixMilli(), h.trade.flattens[2].DeadlineAt)
	require.Equal(t, newT.Add(-2*time.Hour), h.trade.launchedAt[2])
	requireLaunchedBeforeDeadline(t, h.trade)
}

// 中 1 场景 C：第 1 轮的行已建好、Trade 正忙还没发起时推迟 3 天，之后 Trade 一直忙到旧的 T 之后。
// 旧行不再按旧的截止时间发起（不会比新计划早好几天卖出，也不会用已过去的截止时间发起），到新的 T−24h 随整轮重开，用新的截止时间发起。
func TestPostponedBeforeFirstRoundLaunched(t *testing.T) {
	h := newHarness(t)
	oldT := baseTime.Add(30 * time.Hour)
	newT := oldT.Add(72 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.trade.mu.Lock()
	h.trade.busyLeft["crypto/la-1"] = 1_000_000
	h.trade.mu.Unlock()
	h.step("e1", oldT.Add(-24*time.Hour))
	require.Zero(t, h.trade.flattenCount())

	mergeAt := oldT.Add(-24*time.Hour + 10*time.Second)
	h.reschedule("e1", newT, "ALL", mergeAt)
	calls := h.trade.callCount("Flatten")
	outcome := driveUntil(t, h, "e1", mergeAt, oldT.Add(time.Hour))
	require.Equal(t, calls, h.trade.callCount("Flatten"), "改期后旧行不再发起")
	require.Equal(t, newT.Add(-24*time.Hour), outcome.NextRunAt)
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistPending, row.Status)
	require.Equal(t, oldT.UnixMilli(), row.PlannedDelistAt)
	require.Empty(t, h.failedSteps(), "等新计划期间没有失败的行")

	h.trade.mu.Lock()
	h.trade.busyLeft["crypto/la-1"] = 0
	h.trade.mu.Unlock()
	outcome = driveUntil(t, h, "e1", outcome.NextRunAt, newT.Add(20*time.Minute))
	require.Equal(t, store.StatusSucceeded, outcome.Status, outcome.Error)
	require.Len(t, h.trade.flattens, 1)
	require.Equal(t, newT.UnixMilli(), h.trade.flattens[0].DeadlineAt)
	require.Equal(t, newT.Add(-24*time.Hour), h.trade.launchedAt[0])
	requireLaunchedBeforeDeadline(t, h.trade)
}

// 下架时间提前、新计划这一轮已经到点：旧行在这一次推进里刚查到结束，就在同一步里按新时间重开，不等下一次推进。
func TestAdvancedScheduleReopensInSameStep(t *testing.T) {
	h := newHarness(t)
	oldT := baseTime.Add(30 * time.Hour)
	newT := oldT.Add(-time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.finalFor = func(request action.FlattenRequest) (string, string) {
		if request.DeadlineAt == oldT.UnixMilli() {
			return action.OperatorPartial, "最近一笔被交易所拒绝"
		}
		return action.OperatorCompleted, ""
	}
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.step("e1", oldT.Add(-24*time.Hour))
	h.reschedule("e1", newT, "ALL", oldT.Add(-24*time.Hour+10*time.Second))

	h.step("e1", oldT.Add(-24*time.Hour+30*time.Second))
	require.Equal(t, 2, h.trade.flattenCount(), "旧行查到 PARTIAL 的同一步里重开")
	require.Equal(t, newT.UnixMilli(), h.trade.flattens[1].DeadlineAt)
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistRunning, row.Status)
	require.Equal(t, newT.UnixMilli(), row.PlannedDelistAt)
}

// 按新时间重开时，不在新持有者名单里的组合留下的旧计划 PARTIAL 行一并收掉，关注项不会一直报到核对。
func TestReopenRetiresRowsOfFormerHolders(t *testing.T) {
	h := newHarness(t)
	oldT := baseTime.Add(30 * time.Hour)
	newT := oldT.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"), live("crypto", "la-2"))
	h.trade.finalFor = func(request action.FlattenRequest) (string, string) {
		if request.DeadlineAt == oldT.UnixMilli() {
			return action.OperatorPartial, "最近一笔被交易所拒绝"
		}
		return action.OperatorCompleted, ""
	}
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.step("e1", oldT.Add(-24*time.Hour))
	h.step("e1", oldT.Add(-24*time.Hour+30*time.Second))
	require.Equal(t, store.DelistPartial, h.steps("e1")[summaryKey(RoundFlatten)].Status)
	require.Len(t, h.failedSteps(), 1)

	h.reschedule("e1", newT, "ALL", oldT.Add(-23*time.Hour))
	h.trade.setHolders(live("crypto", "la-1")) // la-2 已经被策略自己卖掉
	h.step("e1", newT.Add(-24*time.Hour))
	former := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-2")]
	require.Equal(t, store.DelistSucceeded, former.Status)
	require.Contains(t, former.Result, "已不再持有")
	require.Contains(t, former.Result, store.DelistPartial, "旧结果留在说明里")
	require.Equal(t, newT.UnixMilli(), former.PlannedDelistAt)

	h.step("e1", newT.Add(-24*time.Hour+30*time.Second))
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[summaryKey(RoundFlatten)].Status)
	require.Empty(t, h.failedSteps())
}

// 小 2：Trade 一直正忙时，从第一次忙起持续 30 分钟还没能发起就算暂时失败，工作线程累计连续次数，关注项能据此提醒；
// 能发起之后清掉第一次忙的时间，连续次数清零。
func TestLongBusyCountsAsTransientFailure(t *testing.T) {
	h := newHarness(t)
	run := h.throughWorker()
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.mu.Lock()
	h.trade.busyLeft["crypto/la-1"] = 1_000_000
	h.trade.mu.Unlock()
	h.addExecution("e1", delistAt, "ALL")
	run("e1", baseTime)
	start := delistAt.Add(-24 * time.Hour)
	execution := run("e1", start)
	require.Equal(t, start.UnixMilli(), h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].BusySince)
	for at := start.Add(30 * time.Second); at.Before(start.Add(30 * time.Minute)); at = at.Add(30 * time.Second) {
		execution = run("e1", at)
		require.Zero(t, execution.Failures, "忙不满 30 分钟是正常等待")
	}
	execution = run("e1", start.Add(30*time.Minute))
	require.Equal(t, 1, execution.Failures)
	require.Contains(t, execution.LastError, "一直正忙")
	for at := start.Add(30*time.Minute + 30*time.Second); !at.After(start.Add(46 * time.Minute)); at = at.Add(30 * time.Second) {
		execution = run("e1", at)
	}
	require.Equal(t, start.Add(30*time.Minute).UnixMilli(), execution.FirstFailedAt)
	stuck, err := h.store.ListLongTransientFailures(context.Background(), start.Add(31*time.Minute).UnixMilli())
	require.NoError(t, err)
	require.Len(t, stuck, 1, "持续暂时失败超过 15 分钟，关注项会提醒")

	h.trade.mu.Lock()
	h.trade.busyLeft["crypto/la-1"] = 0
	h.trade.mu.Unlock()
	execution = run("e1", start.Add(46*time.Minute+30*time.Second))
	require.Zero(t, execution.Failures)
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistRunning, row.Status)
	require.Zero(t, row.BusySince)
}

// 小 3：发起平仓被 Trade 拒绝参数（INVALID_PARAM、TRADE_ADOPTION_REQUIRED）时这一行直接失败，不再退避重试。
func TestFlattenRejectedFailsRowWithoutRetry(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.setErr("Flatten", fmt.Errorf("%w: TRADE_ADOPTION_REQUIRED", action.ErrInvalidRequest))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	outcome := h.step("e1", delistAt.Add(-24*time.Hour))
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistFailed, row.Status)
	require.Contains(t, row.Error, "被 Trade 拒绝")
	require.Zero(t, row.Attempts, "不计退避重试")
	require.Equal(t, delistAt.Add(-2*time.Hour), outcome.NextRunAt, "没有再试的唤醒，下一次是第 2 轮")
	require.Equal(t, store.DelistFailed, h.steps("e1")[summaryKey(RoundFlatten)].Status)

	h.step("e1", delistAt.Add(-24*time.Hour+10*time.Minute))
	require.Equal(t, 1, h.trade.callCount("Flatten"))
	require.Len(t, h.failedSteps(), 1, "关注项提醒人工处理")
}

// 小 4：演练与同标的真实下架同时存在，名单项是真实下架写的，演练结束时不解除、名单一直都在。演练最后一步进行中合并了新的登记
// （结果没写进去）：按核对行记下的下架时间判断是重开，清掉上一次演练的步骤、按新的下架时间重走时间线。
func TestDrillReopenedWhileRealDelistHoldsBlock(t *testing.T) {
	h := newHarness(t)
	h.addExecution("real", baseTime.Add(200*time.Hour), "ALL")
	h.step("real", baseTime)
	require.True(t, h.trade.isBlocked())
	require.True(t, strings.Contains(h.trade.blocked[foo], blockOwnerMarker("real")))

	drillT := baseTime.Add(3 * time.Hour)
	h.trade.setHolders(paper("crypto", "la-p"))
	h.addExecution("drill", drillT, "PAPER_ONLY")
	h.step("drill", baseTime) // 写名单（已在名单里），第 1 轮已过时间，立刻发起
	h.step("drill", baseTime.Add(31*time.Second))
	h.step("drill", drillT.Add(-2*time.Hour))
	require.Equal(t, 1, h.trade.flattenCount())
	// 直接调处理器、不经工作线程，等价于“结果因为合并了新登记没写进去，记录仍是待处理”。
	require.Equal(t, store.StatusSucceeded, h.step("drill", drillT.Add(10*time.Minute)).Status)
	require.True(t, h.trade.isBlocked(), "名单项是真实下架写的，演练结束时不解除")
	verify := h.steps("drill")[verifyKey()]
	require.Equal(t, drillT.UnixMilli(), verify.PlannedDelistAt, "核对行记下按哪个下架时间核对")

	newDrillT := drillT.Add(48 * time.Hour)
	h.merge("drill", newDrillT)
	blocks := h.trade.callCount("Block")
	outcome := h.step("drill", drillT.Add(11*time.Minute))
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Equal(t, newDrillT.Add(-24*time.Hour), outcome.NextRunAt, "按新的下架时间走")
	require.Equal(t, blocks+1, h.trade.callCount("Block"), "重新写名单")
	require.Len(t, h.steps("drill"), 1, "上一次演练的各轮步骤已清掉")

	h.trade.setHolders(paper("crypto", "la-p"))
	h.step("drill", newDrillT.Add(-24*time.Hour))
	require.Equal(t, 2, h.trade.flattenCount(), "新的一次演练照常平掉新出现的模拟盘持仓")
}

// 小 4：同一个下架时间重新回到待处理（卡住后被放回等）时不清步骤：名单是自己结束时解除的，重新写名单、重新核对，不当作手工解除。
func TestDrillSameScheduleReblocksWithoutRestart(t *testing.T) {
	h := newHarness(t)
	drillT := baseTime.Add(3 * time.Hour)
	h.addExecution("drill", drillT, "PAPER_ONLY")
	h.step("drill", baseTime)
	require.Equal(t, store.StatusSucceeded, h.step("drill", drillT.Add(10*time.Minute)).Status)
	require.False(t, h.trade.isBlocked(), "演练结束时自己解除")
	require.True(t, h.steps("drill")[blockKey()].SelfReleased)

	blocks := h.trade.callCount("Block")
	require.Equal(t, store.StatusSucceeded, h.step("drill", drillT.Add(11*time.Minute)).Status, "不能当作手工解除而置为已取消")
	require.Equal(t, blocks+1, h.trade.callCount("Block"))
	require.Equal(t, 2, h.steps("drill")[verifyKey()].Attempts, "没有清步骤，重新核对了一次")
	require.False(t, h.trade.isBlocked())
}
