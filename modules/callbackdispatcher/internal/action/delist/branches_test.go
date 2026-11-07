package delist

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// 到了平仓时间的推进：先发起，30 秒后查结果。
func flattenAt(h *harness, id string, delistAt time.Time) time.Time {
	h.step(id, baseTime)
	at := delistAt.Add(-24 * time.Hour)
	h.step(id, at)
	return at
}

// Trade 忙时不计重试次数，30 秒后再试，忙了很多次也不会置为失败。
func TestBusyDoesNotCountAsRetry(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.busyLeft["crypto/la-1"] = 15
	h.addExecution("e1", delistAt, "ALL")
	at := flattenAt(h, "e1", delistAt)
	for i := 0; i < 14; i++ {
		at = at.Add(30 * time.Second)
		outcome := h.step("e1", at)
		require.Equal(t, store.StatusPending, outcome.Status)
		require.Equal(t, at.Add(30*time.Second), outcome.NextRunAt)
	}
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistPending, row.Status)
	require.Zero(t, row.Attempts, "忙不计次数")
	require.Zero(t, h.trade.flattenCount())

	h.step("e1", at.Add(30*time.Second))
	require.Equal(t, 1, h.trade.flattenCount())
	h.step("e1", at.Add(time.Minute))
	row = h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistSucceeded, row.Status)
	require.Empty(t, row.Error)
}

// FAILED 同一个操作号按指数退避重试，用完 10 次这个组合这一轮置为失败，但执行记录不失败：第 2 轮和核对照常进行，并产生 action_failed。
func TestFailedRetriesBackoffThenOtherRoundsContinue(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	failing := true
	h.trade.finalFor = func(action.FlattenRequest) (string, string) {
		if failing {
			return action.OperatorFailed, "交易所拒单"
		}
		return action.OperatorCompleted, ""
	}
	at := flattenAt(h, "e1", delistAt)
	delays := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	var firstID string
	for index := 0; index < 10; index++ {
		at = at.Add(30 * time.Second)
		h.step("e1", at) // 查到失败
		row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
		require.Equal(t, index+1, row.Attempts)
		if firstID == "" {
			firstID = row.OperatorActionID
		}
		require.Equal(t, firstID, row.OperatorActionID, "失败用同一个操作号重试")
		if index == 9 {
			// 第 10 次失败：这个组合这一轮置为失败。
			require.Equal(t, store.DelistFailed, row.Status)
			break
		}
		require.Equal(t, store.DelistPending, row.Status)
		require.Equal(t, at.Add(delays[index]).UnixMilli(), row.NextCheckAt, "第 %d 次失败后的退避", index+1)
		at = at.Add(delays[index])
		h.step("e1", at) // 同一个操作号重新发起
		require.Equal(t, store.DelistRunning, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)
		require.Equal(t, 1, h.trade.flattenCount(), "同号重试由 Trade 幂等")
	}
	require.Equal(t, store.DelistFailed, h.steps("e1")[summaryKey(RoundFlatten)].Status)

	// 执行记录没有失败：关注项里有它。
	execution, err := h.store.GetExecution(ctx, "e1")
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, execution.Status)
	failedSteps, err := h.store.ListUnfinishedWithFailedSteps(ctx)
	require.NoError(t, err)
	require.Len(t, failedSteps, 1)

	// 第 2 轮照常进行（换新操作号），这次成功。
	failing = false
	h.step("e1", delistAt.Add(-2*time.Hour))
	h.step("e1", delistAt.Add(-2*time.Hour+30*time.Second))
	review := h.steps("e1")[flattenKey(RoundReview, "crypto", "la-1")]
	require.Equal(t, store.DelistSucceeded, review.Status)
	require.NotEqual(t, firstID, review.OperatorActionID)
	require.Equal(t, store.StatusSucceeded, h.step("e1", delistAt.Add(10*time.Minute)).Status)
}

// 发起平仓本身出错（Trade 不可达）也按计数的失败退避重试，不是忙。
func TestFlattenCallErrorCountsAsRetry(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.trade.setErr("Flatten", errors.New("连接被拒绝"))
	at := flattenAt(h, "e1", delistAt)
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, 1, row.Attempts)
	require.Contains(t, row.Error, "连接被拒绝")
	require.Equal(t, at.Add(time.Minute).UnixMilli(), row.NextCheckAt)
}

// 仅模拟盘：Trade 违约（返回了非模拟盘的持有者）时，处理器自己过滤，不平实盘。
func TestPaperOnlyFiltersNonPaperHolders(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.ignorePaperOnly = true
	h.trade.setHolders(live("crypto", "la-live"), paper("paper-space", "la-paper"))
	h.addExecution("e1", delistAt, "PAPER_ONLY")
	flattenAt(h, "e1", delistAt)
	h.step("e1", delistAt.Add(-24*time.Hour+30*time.Second))
	require.Equal(t, 1, h.trade.flattenCount())
	require.Equal(t, "la-paper", h.trade.flattens[0].LogicalAccountID)
	require.NotContains(t, h.steps("e1"), flattenKey(RoundFlatten, "crypto", "la-live"))

	// 核对时实盘仍持有也不算：这条记录只管模拟盘。
	require.Equal(t, store.StatusSucceeded, h.step("e1", delistAt.Add(10*time.Minute)).Status)
}

// 范围 ALL：实盘和模拟盘都处理。
func TestScopeAllFlattensEveryHolder(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-live"), paper("paper-space", "la-paper"))
	h.addExecution("e1", delistAt, "ALL")
	flattenAt(h, "e1", delistAt)
	require.Equal(t, 2, h.trade.flattenCount())
}

// 未知标的：写入名单后直接结束，不发 delist_detected，不查持有者。
func TestUnknownInstrumentFinishesImmediately(t *testing.T) {
	h := newHarness(t)
	h.trade.unknown = true
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	before := len(telemetry.Recent(telemetry.AttentionDelistDetected, baseTime, time.Minute))
	outcome := h.step("e1", baseTime)
	require.Equal(t, store.StatusSucceeded, outcome.Status)
	require.True(t, h.trade.isBlocked(), "名单照样保留")
	require.Zero(t, h.trade.callCount("ListHolders"))
	require.Equal(t, before, len(telemetry.Recent(telemetry.AttentionDelistDetected, baseTime, time.Minute)))
	require.Contains(t, h.steps("e1")[blockKey()].Result, "没有这个标的")
}

// 零头只告警不处理：不平仓，核对通过，零头列在结果里并告警一次；有非零头持有时只平非零头。
func TestDustIsReportedNotFlattened(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	dust := action.Holder{SpaceID: "crypto", LogicalAccountID: "la-dust", Exchange: "binance", Quantity: "0.0001", Dust: true}
	h.trade.setHolders(live("crypto", "la-1"), dust)
	h.addExecution("e1", delistAt, "ALL")
	flattenAt(h, "e1", delistAt)
	require.Equal(t, 1, h.trade.flattenCount(), "零头不平")
	h.step("e1", delistAt.Add(-24*time.Hour+30*time.Second))
	require.Contains(t, h.steps("e1")[summaryKey(RoundFlatten)].Result, "零头（不处理）：crypto/la-dust 数量 0.0001")

	outcome := h.step("e1", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusSucceeded, outcome.Status)
	require.Contains(t, h.steps("e1")[verifyKey()].Result, "la-dust")
	notes := telemetry.Recent(telemetry.AttentionActionFailed, delistAt.Add(10*time.Minute), time.Minute)
	require.Len(t, notes, 1)
	require.Contains(t, notes[0], "零头")
}

// 核对时仍有非零头持有：记 VERIFY 失败。
func TestVerifyFailsWhenStillHeld(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.trade.finalFor = func(action.FlattenRequest) (string, string) {
		return action.OperatorPartial, "最近一笔被交易所拒绝"
	}
	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))
	outcome := h.step("e1", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusFailed, outcome.Status)
	require.Contains(t, outcome.Error, "仍有 1 个组合持有")
	require.Equal(t, store.DelistFailed, h.steps("e1")[verifyKey()].Status)
}

// 禁止买入失败：暂时性错误保留步骤的失败说明并稍后重试；Trade 拒绝参数则直接失败。
func TestBlockFailures(t *testing.T) {
	h := newHarness(t)
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.trade.setErr("Block", errors.New("连接被拒绝"))
	outcome := h.step("e1", baseTime)
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Equal(t, baseTime.Add(time.Minute), outcome.NextRunAt)
	require.Contains(t, outcome.Error, "写入下架名单失败")
	row := h.steps("e1")[blockKey()]
	require.Equal(t, store.DelistPending, row.Status)
	require.Equal(t, 1, row.Attempts)
	require.Contains(t, row.Error, "连接被拒绝")

	h.trade.setErr("Block", nil)
	outcome = h.step("e1", baseTime.Add(time.Minute))
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Empty(t, outcome.Error)
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[blockKey()].Status)

	h2 := newHarness(t)
	h2.addExecution("e2", baseTime.Add(48*time.Hour), "ALL")
	h2.trade.setErr("Block", action.ErrInvalidRequest)
	outcome = h2.step("e2", baseTime)
	require.Equal(t, store.StatusFailed, outcome.Status)
	require.Equal(t, store.DelistFailed, h2.steps("e2")[blockKey()].Status)
}

// 读名单、查持有者出错：稍后重试，不丢状态。
func TestTransientTradeErrorsRetryLater(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)

	h.trade.setErr("ListBlocked", errors.New("Trade 不可达"))
	outcome := h.step("e1", baseTime.Add(time.Hour))
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Equal(t, baseTime.Add(time.Hour+time.Minute), outcome.NextRunAt)
	require.Contains(t, outcome.Error, "读取下架名单失败")
	require.Zero(t, h.trade.flattenCount())
	h.trade.setErr("ListBlocked", nil)

	h.trade.setErr("ListHolders", errors.New("Trade 不可达"))
	at := delistAt.Add(-24 * time.Hour)
	outcome = h.step("e1", at)
	require.Equal(t, at.Add(time.Minute), outcome.NextRunAt)
	require.NotContains(t, h.steps("e1"), summaryKey(RoundFlatten), "没查到持有者就不记这一轮已开始")
	h.trade.setErr("ListHolders", nil)
	h.step("e1", at.Add(time.Minute))
	require.Equal(t, 1, h.trade.flattenCount())
}

// 一次 Step 的剩余时间不够时不再发起新的 Trade 调用，尽快回来。
func TestLowBudgetDefersTradeCalls(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	at := delistAt.Add(-24 * time.Hour)
	h.clock.Set(at)
	execution, err := h.store.GetExecution(context.Background(), "e1")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	outcome, err := h.handler.Step(ctx, execution, at)
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Equal(t, at.Add(5*time.Second), outcome.NextRunAt)
	require.Zero(t, h.trade.flattenCount())
}

// 取消之后 Step 不再发起新的平仓。
func TestCanceledExecutionStopsFlatten(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	at := delistAt.Add(-24 * time.Hour)
	h.clock.Set(at)
	execution, err := h.store.GetExecution(ctx, "e1")
	require.NoError(t, err)
	_, err = h.store.CancelExecution(ctx, "e1", at.UnixMilli())
	require.NoError(t, err)
	outcome, err := h.handler.Step(ctx, execution, at)
	require.NoError(t, err)
	require.Equal(t, store.StatusCanceled, outcome.Status)
	require.Zero(t, h.trade.flattenCount())
}
