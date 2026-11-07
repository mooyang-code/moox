package delist

// C6 第 1 轮审查的回归用例（R1~R6 中与禁止名单有关的几条）。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

// addRaw 新建一条记录，代码按给定写法写进事件内容（手工登记时用户可能输入小写）；subject 与接收端一致，是统一后的写法。
func (h *harness) addRaw(id, symbol string, delistAt time.Time, scope string) {
	h.t.Helper()
	subject := "binance:SPOT:FOOUSDT"
	payload := fmt.Sprintf(`{"exchange":"binance","market":"SPOT","exchange_symbol":%q,"delist_at":%q,"source":"MANUAL","scope":%q}`,
		symbol, delistAt.UTC().Format(time.RFC3339), scope)
	execution := store.Execution{
		ExecutionID: id, RuleID: "sys-1", RuleName: "规则", ActionType: store.ActionSystem, EventID: "event-" + id,
		EventName: "event.collector.instrument.delist_scheduled", SpaceID: "mooxsys", SubjectID: subject,
		OccurredAt: baseTime.UnixMilli(), PayloadJSON: payload, Status: store.StatusPending,
		DedupeKey: "sys-1|" + subject + "|" + scope, NextRunAt: baseTime.UnixMilli(), CreatedAt: 1, UpdatedAt: 1,
	}
	require.NoError(h.t, h.store.InTx(context.Background(), func(tx *store.Tx) error { return tx.InsertExecution(execution) }))
}

// finish 把一条记录在库里置为终态（其余被领取的放回待处理）。
func (h *harness) finish(id, status string) {
	h.t.Helper()
	claimed, err := h.store.ClaimDue(context.Background(), time.Now().Add(1000*time.Hour).UnixMilli(), 10, []string{store.SystemActionSellDelistedHoldings})
	require.NoError(h.t, err)
	found := false
	for _, item := range claimed {
		result := store.Result{Status: store.StatusPending, NextRunAt: item.NextRunAt}
		if item.ExecutionID == id {
			found = true
			result = store.Result{Status: status, FinishedAt: 1}
		}
		applied, err := h.store.SaveResult(context.Background(), item.ExecutionID, item.ClaimedAt, result, 1)
		require.NoError(h.t, err)
		require.True(h.t, applied)
	}
	require.True(h.t, found)
}

// R1：演练用小写代码登记，与同标的真实下架是同一个标的：演练结束不能解除真实下架的禁止，真实那条继续推进。
func TestLowercaseDrillDoesNotUnblockRealDelist(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-live"), paper("crypto", "la-paper"))
	h.addExecution("real", delistAt, "ALL")
	h.addRaw("drill", "foousdt", baseTime.Add(3*time.Hour), "PAPER_ONLY")
	h.step("real", baseTime)
	h.step("drill", baseTime)
	require.True(t, h.trade.isBlocked())
	h.step("drill", baseTime.Add(time.Hour))
	h.step("drill", baseTime.Add(time.Hour+31*time.Second))
	out := h.step("drill", baseTime.Add(3*time.Hour+11*time.Minute))
	require.Equal(t, store.StatusSucceeded, out.Status)
	require.True(t, h.trade.isBlocked(), "演练结束不解除真实下架的禁止")
	out = h.step("real", baseTime.Add(4*time.Hour))
	require.Equal(t, store.StatusPending, out.Status, "真实下架照常推进，不被误判成已手工解除")
}

// R2：真实下架已结束（名单按设计保留），同标的演练之后结束：不能把真实下架保留的名单解除。
func TestDrillAfterFinishedRealKeepsBlock(t *testing.T) {
	h := newHarness(t)
	h.addExecution("real", baseTime.Add(time.Hour), "ALL")
	h.addExecution("drill", baseTime.Add(3*time.Hour), "PAPER_ONLY")
	h.step("real", baseTime)
	h.step("drill", baseTime)
	out := h.step("real", baseTime.Add(time.Hour+11*time.Minute))
	require.Equal(t, store.StatusSucceeded, out.Status)
	h.finish("real", store.StatusSucceeded)
	require.True(t, h.trade.isBlocked(), "真实下架结束后名单保留")
	out = h.step("drill", baseTime.Add(3*time.Hour+11*time.Minute))
	require.Equal(t, store.StatusSucceeded, out.Status)
	require.True(t, h.trade.isBlocked(), "演练结束不能解除已结束的真实下架保留的名单")
	require.Zero(t, h.trade.callCount("Unblock"))
}

// 演练的名单是别人（真实下架）先写的：演练自己没写，结束和取消都不解除，即使那条真实记录之后被清理。
func TestDrillDoesNotUnblockWhatItDidNotWrite(t *testing.T) {
	h := newHarness(t)
	h.trade.setBlocked(true) // 名单早就有了（例如已结束并被清理的真实下架留下的）
	h.addExecution("drill", baseTime.Add(3*time.Hour), "PAPER_ONLY")
	h.step("drill", baseTime)
	require.False(t, h.steps("drill")[blockKey()].OwnsBlock)
	require.NoError(t, h.cancel("drill"))
	require.True(t, h.trade.isBlocked())
	require.Zero(t, h.trade.callCount("Unblock"))

	h2 := newHarness(t)
	h2.addExecution("drill", baseTime.Add(3*time.Hour), "PAPER_ONLY")
	h2.step("drill", baseTime)
	require.True(t, h2.steps("drill")[blockKey()].OwnsBlock)
	require.NoError(t, h2.cancel("drill"))
	require.False(t, h2.trade.isBlocked(), "自己写的名单，取消时解除")
}

// R4：演练登记了 Trade 拒绝参数的标的：写名单被拒，记录直接失败，不会因为“演练结束解除”同样被拒而永远待处理；取消的善后也能做成。
func TestInvalidDrillFailsOnceAndCancelCleanupCompletes(t *testing.T) {
	h := newHarness(t)
	h.addExecution("drill", baseTime.Add(48*time.Hour), "PAPER_ONLY")
	h.trade.setErr("Block", fmt.Errorf("%w: 不支持的交易所", action.ErrInvalidRequest))
	h.trade.setErr("Unblock", fmt.Errorf("%w: 不支持的交易所", action.ErrInvalidRequest))
	out := h.step("drill", baseTime)
	require.Equal(t, store.StatusFailed, out.Status)
	require.NoError(t, h.cancel("drill"), "Trade 拒绝参数说明名单里不会有它，善后当作没有要解除的")

	h2 := newHarness(t)
	h2.addExecution("real", baseTime.Add(48*time.Hour), "ALL")
	h2.trade.setErr("Block", fmt.Errorf("%w: 不支持的交易所", action.ErrInvalidRequest))
	h2.trade.setErr("Unblock", fmt.Errorf("%w: 不支持的交易所", action.ErrInvalidRequest))
	require.Equal(t, store.StatusFailed, h2.step("real", baseTime).Status)
	require.NoError(t, h2.cancel("real"))
}

// 从没尝试过写名单的记录（还没推进过就被取消），取消时不去碰名单。
func TestCancelBeforeAnyStepDoesNotTouchBlock(t *testing.T) {
	h := newHarness(t)
	h.trade.setBlocked(true)
	h.addExecution("real", baseTime.Add(48*time.Hour), "ALL")
	require.NoError(t, h.cancel("real"))
	require.True(t, h.trade.isBlocked())
	require.Zero(t, h.trade.callCount("Unblock"))
}

// 写名单后复查记录状态失败：先记下“名单已写”，记录其实已取消时标记善后未完成，之后重试善后能把名单解除。
func TestRecheckFailureLeavesRetryableCleanup(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.trade.blockHook = func(context.Context) {
		_, err := h.store.CancelExecution(ctx, "e1", baseTime.UnixMilli())
		require.NoError(t, err)
		require.NoError(t, h.store.FinishCleanup(ctx, "e1", baseTime.UnixMilli())) // OnCancel 已经先做完了
	}
	h.handler.loadExecution = func(context.Context, string) (store.Execution, error) {
		return store.Execution{}, errors.New("数据库繁忙")
	}
	h.clock.Set(baseTime)
	out, err := h.handler.Step(ctx, h.execution("e1"), baseTime)
	require.NoError(t, err)
	require.Equal(t, store.StatusPending, out.Status)
	got := h.execution("e1")
	require.Equal(t, store.StatusCanceled, got.Status)
	require.True(t, got.CleanupPending, "善后可能没做完，用户能看到“重试善后”")
	require.True(t, h.trade.isBlocked())
	require.NoError(t, h.handler.OnCancel(ctx, got))
	require.False(t, h.trade.isBlocked())
}
