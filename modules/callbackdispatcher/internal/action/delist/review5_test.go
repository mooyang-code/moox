package delist

// 最后一轮复审的回归用例：演练重开后的重开序号不撞号、重开规划写到一半退出后重入、重开前查到旧计划已全部成功就不再重开。

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

// 演练 T=A 第 1 轮按 (A,0) 发起，改期到更早的 X 后按 (X,1) 重开；核对与解除期间又来一次 T=A 的演练登记合并进来，记录从头重走。
// 新一次演练的第 1 轮不能从序号 0 算起：(A,0) 是上一次演练用过的号，Trade 按同号幂等直接返回旧的终态，这一轮就没有真的平。
func TestDrillRestartDoesNotReuseEarlierReopenSeq(t *testing.T) {
	h := newHarness(t)
	run := h.throughWorker()
	timeA := baseTime.Add(50 * time.Hour)
	timeX := timeA.Add(-time.Hour)
	h.trade.setHolders(paper("crypto", "la-p"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorPartial, "拒单" }
	h.addExecution("drill", timeA, "PAPER_ONLY")
	run("drill", baseTime)

	first := timeA.Add(-24 * time.Hour)
	run("drill", first)
	run("drill", first.Add(31*time.Second))
	require.Equal(t, store.DelistPartial, h.steps("drill")[summaryKey(RoundFlatten)].Status)
	require.Zero(t, h.steps("drill")[summaryKey(RoundFlatten)].ReopenSeq)

	// 改到更早的 X：X 计划的第 1 轮已到点，按 (X,1) 重开。
	h.reschedule("drill", timeX, "PAPER_ONLY", first.Add(time.Minute))
	run("drill", first.Add(time.Minute))
	run("drill", first.Add(time.Minute+31*time.Second))
	require.Equal(t, 1, h.steps("drill")[summaryKey(RoundFlatten)].ReopenSeq)
	run("drill", timeX.Add(-2*time.Hour))
	run("drill", timeX.Add(-2*time.Hour+31*time.Second))
	require.Equal(t, 3, h.trade.flattenCount())

	// 核对失败，结束时解除名单期间又来一次 T=A 的演练登记：结果不写，记录改回待处理。
	var once sync.Once
	h.trade.unblockHook = func(context.Context) error {
		once.Do(func() { h.merge("drill", timeA) })
		return nil
	}
	execution := run("drill", timeX.Add(10*time.Minute))
	require.Equal(t, store.StatusPending, execution.Status)
	require.Equal(t, store.DelistFailed, h.steps("drill")[verifyKey()].Status)

	// 从头重走：A 计划的第 1 轮和复查都已到点。各轮第一次规划从上一次用过的最大序号加 1 起，操作号与上一次演练的都不同。
	h.trade.finalFor = nil
	execution = run("drill", timeX.Add(10*time.Minute))
	require.Equal(t, store.StatusPending, execution.Status)
	row := h.steps("drill")[flattenKey(RoundFlatten, "crypto", "la-p")]
	require.NotEqual(t, h.trade.flattens[0].ActionID, row.OperatorActionID, "第 1 轮不能用回上一次演练 (A,0) 的号")
	require.Equal(t, operatorActionID("drill", RoundFlatten, "crypto", "la-p", timeA.UnixMilli(), 2), row.OperatorActionID)
	require.Equal(t, 2, row.ReopenSeq)
	require.Equal(t, 2, h.steps("drill")[summaryKey(RoundFlatten)].ReopenSeq)
	require.Equal(t, 2, h.steps("drill")[blockKey()].NextReopenSeq, "上一次演练用过的最大序号是 1")
	launched := map[string]bool{}
	for _, request := range h.trade.flattens[3:] {
		launched[request.ActionID] = true
	}
	require.True(t, launched[row.OperatorActionID], "第 1 轮真的又发起了一次清仓")
	for _, earlier := range h.trade.flattens[:3] {
		require.False(t, launched[earlier.ActionID], "新一次演练的号都与上一次演练的不同")
	}

	run("drill", timeX.Add(10*time.Minute+31*time.Second))
	require.Equal(t, store.DelistSucceeded, h.steps("drill")[flattenKey(RoundFlatten, "crypto", "la-p")].Status)
	execution = run("drill", timeA.Add(10*time.Minute))
	require.Equal(t, store.StatusSucceeded, execution.Status, execution.LastError)
}

// failSummaryWrite 让写某个重开序号的汇总行失败，模拟“各组合行已改写、汇总行还没写”时进程退出；返回撤掉注入的函数。
func (h *harness) failSummaryWrite(reopenSeq int) func() {
	h.t.Helper()
	require.NoError(h.t, h.store.ApplySchema(`CREATE TRIGGER trg_test_fail_summary
BEFORE INSERT ON t_delist_actions
FOR EACH ROW
WHEN NEW.c_kind = 'FLATTEN' AND NEW.c_space_id = '' AND NEW.c_logical_account_id = '' AND NEW.c_reopen_seq = `+strconv.Itoa(reopenSeq)+`
BEGIN
    SELECT RAISE(ABORT, '模拟写汇总行时退出');
END`))
	return func() { require.NoError(h.t, h.store.ApplySchema(`DROP TRIGGER trg_test_fail_summary`)) }
}

// 改期重开时各组合行已按 (X,1) 改写、写汇总行之前退出：重入时旧汇总行 (A,0) 还没有终态，它的计划里已经没有组合行。
// 不能把它结算成成功（那样这一轮就不再重开，X 的新行永远不会发起），要接着按重开走完规划，按时发起。
func TestInterruptedReopenPlanningResumesOnReentry(t *testing.T) {
	h := newHarness(t)
	timeA := baseTime.Add(30 * time.Hour)
	timeX := timeA.Add(-time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.mu.Lock()
	h.trade.busyLeft["crypto/la-1"] = 1
	h.trade.mu.Unlock()
	h.addExecution("e1", timeA, "ALL")
	h.step("e1", baseTime)
	first := timeA.Add(-24 * time.Hour)
	h.step("e1", first)
	require.Zero(t, h.trade.flattenCount(), "Trade 正忙，旧计划的行还没发起")
	require.Equal(t, store.DelistRunning, h.steps("e1")[summaryKey(RoundFlatten)].Status)

	h.reschedule("e1", timeX, "ALL", first.Add(10*time.Second))
	restore := h.failSummaryWrite(1)
	execution, err := h.store.GetExecution(context.Background(), "e1")
	require.NoError(t, err)
	h.clock.Set(first.Add(10 * time.Second))
	_, err = h.handler.Step(context.Background(), execution, first.Add(10*time.Second))
	require.Error(t, err, "写汇总行失败")
	steps := h.steps("e1")
	require.Equal(t, timeA.UnixMilli(), steps[summaryKey(RoundFlatten)].PlannedDelistAt, "汇总行还是旧计划")
	require.Zero(t, steps[summaryKey(RoundFlatten)].ReopenSeq)
	row := steps[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, timeX.UnixMilli(), row.PlannedDelistAt, "组合行已按新计划改写")
	require.Equal(t, 1, row.ReopenSeq)
	restore()

	h.step("e1", first.Add(20*time.Second))
	require.Equal(t, 1, h.trade.flattenCount(), "重入后按新计划发起")
	require.Equal(t, timeX.UnixMilli(), h.trade.flattens[0].DeadlineAt)
	require.Equal(t, row.OperatorActionID, h.trade.flattens[0].ActionID, "沿用已存下的操作号")
	summary := h.steps("e1")[summaryKey(RoundFlatten)]
	require.Equal(t, timeX.UnixMilli(), summary.PlannedDelistAt)
	require.Equal(t, 1, summary.ReopenSeq)
	require.Equal(t, store.DelistRunning, summary.Status)
}

// 发起时响应丢了、等重发期间改了期：重开前按操作号查到旧的清仓其实已经完成，这一轮就算成功，先结算并返回，
// 不再查持有者重开，也不消耗重开序号。
func TestReopenProbeFindsCompletedAndSettles(t *testing.T) {
	h := newHarness(t)
	timeA := baseTime.Add(30 * time.Hour)
	timeX := timeA.Add(-time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.loseFlattenResponses = 1
	h.addExecution("e1", timeA, "ALL")
	h.step("e1", baseTime)
	first := timeA.Add(-24 * time.Hour)
	h.step("e1", first)
	require.Equal(t, store.DelistPending, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)
	require.Equal(t, 1, h.trade.flattenCount())

	h.reschedule("e1", timeX, "ALL", first.Add(10*time.Second))
	holderQueries := h.trade.callCount("ListHolders")
	h.step("e1", first.Add(10*time.Second))
	steps := h.steps("e1")
	summary := steps[summaryKey(RoundFlatten)]
	require.Equal(t, store.DelistSucceeded, summary.Status, "旧计划的清仓已完成，这一轮成功")
	require.Zero(t, summary.ReopenSeq, "没有重开，不消耗序号")
	require.Equal(t, timeA.UnixMilli(), summary.PlannedDelistAt)
	require.Equal(t, store.DelistSucceeded, steps[flattenKey(RoundFlatten, "crypto", "la-1")].Status)
	require.Equal(t, holderQueries, h.trade.callCount("ListHolders"), "不再查持有者")
	require.Equal(t, 1, h.trade.flattenCount(), "不再发起新的清仓")
}
