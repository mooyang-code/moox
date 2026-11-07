package delist

// 第三轮复审的回归用例：改回原来的下架时间后重开要换操作号；发起时响应丢失后改期，重开前先按操作号向 Trade 确认。

import (
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

// 下架时间 A→B→A：B 计划这一轮已经重开过，再改回 A 时重开的号不能与第一次相同。
// 否则 Trade 按同号幂等直接返回第一次已结束的结果，这一轮就没有真的再平一次（第一次如果是 COMPLETED，还会被当成已经平掉）。
func TestRescheduleBackToOriginalTimeUsesNewActionID(t *testing.T) {
	h := newHarness(t)
	timeA := baseTime.Add(50 * time.Hour)
	timeB := timeA.Add(-time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) {
		return action.OperatorPartial, "最近一笔被交易所拒绝"
	}
	h.addExecution("e1", timeA, "ALL")
	h.step("e1", baseTime)

	first := timeA.Add(-24 * time.Hour)
	h.step("e1", first)
	h.step("e1", first.Add(31*time.Second))
	require.Equal(t, store.DelistPartial, h.steps("e1")[summaryKey(RoundFlatten)].Status)

	// 改到 B（提前 1 小时）：B 计划的第 1 轮已到点，立即重开，序号 1。
	h.reschedule("e1", timeB, "ALL", first.Add(time.Minute))
	h.step("e1", first.Add(time.Minute))
	h.step("e1", first.Add(time.Minute+31*time.Second))
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, timeB.UnixMilli(), row.PlannedDelistAt)
	require.Equal(t, 1, row.ReopenSeq)
	require.Equal(t, store.DelistPartial, row.Status)

	// 改回 A：再重开一次，序号 2，操作号与第一次不同，Trade 上真的又发起了一次清仓。
	h.reschedule("e1", timeA, "ALL", first.Add(2*time.Minute))
	h.step("e1", first.Add(2*time.Minute))
	require.Equal(t, 3, h.trade.flattenCount(), "改回原来的下架时间后真的再发起一次")
	ids := map[string]bool{}
	for _, request := range h.trade.flattens {
		ids[request.ActionID] = true
	}
	require.Len(t, ids, 3, "三次规划的操作号各不相同")
	require.NotEqual(t, h.trade.flattens[0].ActionID, h.trade.flattens[2].ActionID)
	require.Equal(t, timeA.UnixMilli(), h.trade.flattens[2].DeadlineAt)
	row = h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, 2, row.ReopenSeq)
	require.Equal(t, operatorActionID("e1", RoundFlatten, "crypto", "la-1", timeA.UnixMilli(), 2), row.OperatorActionID)
	require.Equal(t, 2, h.steps("e1")[summaryKey(RoundFlatten)].ReopenSeq)

	// 同一行重发沿用存下的号：查结果、重入都不换号。
	h.step("e1", first.Add(2*time.Minute))
	require.Equal(t, 3, h.trade.flattenCount())
	require.Equal(t, row.OperatorActionID, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].OperatorActionID)
}

// 发起时响应丢了（Trade 已建好操作、正在卖，调用方只拿到错误），等重发期间改了期：
// 重开之前按存下的操作号查到它还在运行，按运行中跟踪，结束之后才按新时间重开，不会有两笔清仓同时卖同一个组合。
func TestLostFlattenResponseIsTrackedBeforeReopen(t *testing.T) {
	h := newHarness(t)
	timeA := baseTime.Add(30 * time.Hour)
	timeB := timeA.Add(-time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.loseFlattenResponses = 1
	polls := 0
	h.trade.finalFor = func(request action.FlattenRequest) (string, string) {
		if request.DeadlineAt == timeA.UnixMilli() {
			polls++
			if polls < 3 {
				return action.OperatorRunning, ""
			}
			return action.OperatorPartial, "最近一笔被交易所拒绝"
		}
		return action.OperatorCompleted, ""
	}
	h.addExecution("e1", timeA, "ALL")
	h.step("e1", baseTime)
	first := timeA.Add(-24 * time.Hour)
	h.step("e1", first)
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistPending, row.Status, "响应丢了，记一次失败等重发")
	require.Equal(t, 1, row.Attempts)
	require.Equal(t, 1, h.trade.flattenCount(), "Trade 上其实已经建了操作")

	// 改到 B：B 计划的第 1 轮已到点。重开前查到旧号还在运行，不发起新的清仓。
	h.reschedule("e1", timeB, "ALL", first.Add(10*time.Second))
	h.step("e1", first.Add(10*time.Second))
	require.Equal(t, 1, h.trade.flattenCount(), "旧的清仓还在跑，不重开")
	row = h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistRunning, row.Status, "按运行中跟踪")
	require.Equal(t, timeA.UnixMilli(), row.PlannedDelistAt)

	// 一直查到旧的清仓结束（PARTIAL），之后才按新时间重开，用新的号和新的截止时间。
	outcome := driveUntil(t, h, "e1", first.Add(40*time.Second), timeB.Add(20*time.Minute))
	require.Equal(t, store.StatusSucceeded, outcome.Status, outcome.Error)
	require.Equal(t, 2, h.trade.flattenCount())
	require.Equal(t, timeB.UnixMilli(), h.trade.flattens[1].DeadlineAt)
	require.NotEqual(t, h.trade.flattens[0].ActionID, h.trade.flattens[1].ActionID)
	h.trade.mu.Lock()
	firstOp := h.trade.ops[h.trade.flattens[0].ActionID]
	h.trade.mu.Unlock()
	require.Equal(t, action.OperatorPartial, firstOp.status)
	require.False(t, h.trade.launchedAt[1].Before(first.Add(40*time.Second)), "旧的清仓结束之后才发起新的")
}

// 旧计划里一直正忙、从没发起的行：重开前查 Trade 没有这个操作，照常按新时间重开；查询出错时这次不重开，稍后再查。
func TestReopenProbeNotFoundAndQueryError(t *testing.T) {
	h := newHarness(t)
	timeA := baseTime.Add(30 * time.Hour)
	timeB := timeA.Add(-time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.mu.Lock()
	h.trade.busyLeft["crypto/la-1"] = 1
	h.trade.mu.Unlock()
	h.addExecution("e1", timeA, "ALL")
	h.step("e1", baseTime)
	first := timeA.Add(-24 * time.Hour)
	h.step("e1", first)
	require.Zero(t, h.trade.flattenCount())

	h.reschedule("e1", timeB, "ALL", first.Add(10*time.Second))
	h.trade.setErr("GetAction", errors.New("网关不可达"))
	outcome := h.step("e1", first.Add(10*time.Second))
	require.Zero(t, h.trade.flattenCount(), "查询出错，这次不重开")
	require.Contains(t, outcome.Error, "改期重开前确认旧的平仓有没有在 Trade 上运行失败")

	h.trade.setErr("GetAction", nil)
	h.step("e1", first.Add(time.Minute))
	require.Equal(t, 1, h.trade.flattenCount(), "Trade 没有这个操作，按新时间重开")
	require.Equal(t, timeB.UnixMilli(), h.trade.flattens[0].DeadlineAt)
	require.Equal(t, 1, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].ReopenSeq)
}
