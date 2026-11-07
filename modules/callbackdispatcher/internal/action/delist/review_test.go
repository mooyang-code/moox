package delist

// C6 第 1 轮审查的回归用例（R1~R6 中与禁止名单有关的几条）。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/rpc"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	pb "github.com/mooyang-code/moox/modules/callbackdispatcher/proto/callbackdispatchergen"
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
	require.NoError(t, h.cancel("drill"))
	require.True(t, h.trade.isBlocked())
	require.Zero(t, h.trade.callCount("Unblock"))

	h2 := newHarness(t)
	h2.addExecution("drill", baseTime.Add(3*time.Hour), "PAPER_ONLY")
	h2.step("drill", baseTime)
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
		current := h.execution("e1")
		_, err = h.store.FinishCleanup(ctx, "e1", current.CleanupSeq, baseTime.UnixMilli()) // OnCancel 已经先做完了
		require.NoError(t, err)
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

// R3：服务层做善后期间，Step 写名单后补解除失败又把标记置了位：服务层的“善后完成”不能把它吞掉，名单留着时用户仍看得到“重试善后”。
func TestServiceFinishCleanupDoesNotSwallowStepCompensationFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	registry := action.NewRegistry()
	registry.Register(store.SystemActionSellDelistedHoldings, interleavedHandler{inner: h.handler, onCancelDone: func() {
		// OnCancel 已经做完，但 Step 紧接着写了名单、补解除失败，把标记重新置位。
		applied, err := h.store.MarkCleanupPendingIfCanceled(ctx, "e1", "取消后的善后失败：Trade 超时", baseTime.UnixMilli())
		require.NoError(t, err)
		require.True(t, applied)
	}})
	service := &rpc.Service{Store: h.store, Actions: registry, Now: h.clock.Now}
	_, err := service.CancelEventExecution(ctx, &pb.CancelEventExecutionReq{ExecutionId: "e1"})
	require.NoError(t, err)
	got := h.execution("e1")
	require.True(t, got.CleanupPending, "标记没有被吞掉")
	require.Contains(t, got.LastError, "Trade 超时")
}

type interleavedHandler struct {
	inner        *Handler
	onCancelDone func()
}

func (i interleavedHandler) Step(ctx context.Context, execution store.Execution, now time.Time) (action.Outcome, error) {
	return i.inner.Step(ctx, execution, now)
}

func (i interleavedHandler) OnCancel(ctx context.Context, execution store.Execution) error {
	err := i.inner.OnCancel(ctx, execution)
	i.onCancelDone()
	return err
}

// R5：Trade 把同组合同标的运行中的另一条动作返回给后来者：不能把别人的操作号记成自己的，按忙处理，不计重试次数、不发生幂等冲突。
type adoptTrade struct{ *fakeTrade }

func (a *adoptTrade) FlattenInstrument(ctx context.Context, request action.FlattenRequest) (action.OperatorAction, error) {
	a.mu.Lock()
	if op, ok := a.ops[request.ActionID]; ok && op.request.Reason != request.Reason {
		a.calls["Conflict"]++
		a.mu.Unlock()
		return action.OperatorAction{}, errors.New("Trade 返回 CONFLICT：conflict")
	}
	for id, op := range a.ops {
		if id != request.ActionID && op.status == action.OperatorRunning && op.request.SpaceID == request.SpaceID && op.request.LogicalAccountID == request.LogicalAccountID {
			a.mu.Unlock()
			return action.OperatorAction{ActionID: id, Status: action.OperatorRunning}, nil
		}
	}
	a.mu.Unlock()
	return a.fakeTrade.FlattenInstrument(ctx, request)
}

func TestForeignActionIDIsTreatedAsBusy(t *testing.T) {
	h := newHarness(t)
	trade := &adoptTrade{h.trade}
	h.handler.Trade = trade
	delistAt := baseTime.Add(20 * time.Hour)
	h.trade.setHolders(paper("crypto", "la-paper"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorFailed, "交易所拒绝" }
	h.addExecution("real", delistAt, "ALL")
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("real", baseTime)
	h.step("drill", baseTime)
	realRow := h.steps("real")[flattenKey(RoundFlatten, "crypto", "la-paper")]
	drillRow := h.steps("drill")[flattenKey(RoundFlatten, "crypto", "la-paper")]
	require.NotEqual(t, realRow.OperatorActionID, drillRow.OperatorActionID, "不能记成别人的操作号")
	require.Equal(t, operatorActionID("drill", RoundFlatten, "crypto", "la-paper", delistAt.UnixMilli()), drillRow.OperatorActionID)
	require.Zero(t, drillRow.Attempts, "忙不计次数")
	require.Equal(t, store.DelistPending, drillRow.Status)
	require.Contains(t, drillRow.Error, "已有别的平仓")

	// 真实那条的平仓失败结束之后，演练用自己的号发起，不会发生幂等冲突。
	now := baseTime
	for i := 0; i < 6; i++ {
		now = now.Add(31 * time.Minute)
		h.step("real", now)
		h.step("drill", now)
	}
	require.Zero(t, trade.callCount("Conflict"))
	require.Equal(t, drillRow.OperatorActionID, h.steps("drill")[flattenKey(RoundFlatten, "crypto", "la-paper")].OperatorActionID)
}

// R6：复查之后、核对之前改期到一周后：这两轮都已以只完成一部分结束，按新时间重开，换新的操作号再平，而不是到新时间直接核对失败。
func TestPostponedAfterRoundsFinishedReopensRounds(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	oldT := baseTime.Add(20 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorPartial, "连续未成交" }
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.step("e1", baseTime.Add(31*time.Second))
	h.step("e1", oldT.Add(-2*time.Hour))
	h.step("e1", oldT.Add(-2*time.Hour+31*time.Second))
	h.step("e1", oldT.Add(time.Minute))
	oldIDs := map[int]string{
		RoundFlatten: h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].OperatorActionID,
		RoundReview:  h.steps("e1")[flattenKey(RoundReview, "crypto", "la-1")].OperatorActionID,
	}
	require.Equal(t, store.DelistPartial, h.steps("e1")[summaryKey(RoundFlatten)].Status)
	require.Equal(t, store.DelistPartial, h.steps("e1")[summaryKey(RoundReview)].Status)

	newT := oldT.Add(7 * 24 * time.Hour)
	require.NoError(t, h.store.InTx(ctx, func(tx *store.Tx) error {
		return tx.ReplaceExecutionEvent("e1", "event-2", payloadAt(newT, "ALL"), baseTime.UnixMilli()+1, baseTime.UnixMilli())
	}))
	h.trade.finalFor = nil // 之后 Trade 能平掉
	before := h.trade.flattenCount()
	h.step("e1", newT.Add(-24*time.Hour))
	h.step("e1", newT.Add(-24*time.Hour+31*time.Second))
	require.Equal(t, before+1, h.trade.flattenCount(), "按新时间重开第 1 轮，再平一次")
	row := h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")]
	require.Equal(t, store.DelistSucceeded, row.Status)
	require.NotEqual(t, oldIDs[RoundFlatten], row.OperatorActionID, "重开用新的操作号")
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[summaryKey(RoundFlatten)].Status)
	require.Equal(t, newT.UnixMilli(), h.steps("e1")[summaryKey(RoundFlatten)].PlannedDelistAt)

	// 持有者已经平掉：第 2 轮重开后没有持有者，不再下单；核对通过。
	h.step("e1", newT.Add(-2*time.Hour))
	require.Equal(t, before+1, h.trade.flattenCount())
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[summaryKey(RoundReview)].Status)
	require.Equal(t, store.StatusSucceeded, h.step("e1", newT.Add(11*time.Minute)).Status)
}

// 已经成功的一轮，改期后不重开。
func TestPostponedDoesNotReopenSucceededRound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	oldT := baseTime.Add(20 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", oldT, "ALL")
	h.step("e1", baseTime)
	h.step("e1", baseTime.Add(31*time.Second))
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[summaryKey(RoundFlatten)].Status)
	h.trade.setHolders(live("crypto", "la-1")) // 又出现了持仓
	newT := oldT.Add(72 * time.Hour)
	require.NoError(t, h.store.InTx(ctx, func(tx *store.Tx) error {
		return tx.ReplaceExecutionEvent("e1", "event-2", payloadAt(newT, "ALL"), baseTime.UnixMilli()+1, baseTime.UnixMilli())
	}))
	before := h.trade.flattenCount()
	h.step("e1", newT.Add(-24*time.Hour))
	require.Equal(t, before, h.trade.flattenCount(), "成功的一轮不重开，新持仓留给复查")
}

// T27 对接：Trade 报告没能判断的账户（skipped）时，平仓轮次不能算成功、核对不能因为“没人持有”就通过，都要提醒人工确认。
func TestSkippedAccountsNeedManualConfirmation(t *testing.T) {
	skipped := []action.SkippedAccount{{TradingAccountID: "ta-9", Reason: "资金设置读不出来"}}

	// 查到持有者、同时有没能判断的账户：持有的照常平；这一轮最终至少是“只完成一部分”，带着说明。
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.trade.skipped = skipped
	h.trade.notes = []string{"计价币不是结算币，清仓不会卖它"}
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))
	h.step("e1", delistAt.Add(-24*time.Hour+30*time.Second))
	require.Equal(t, 1, h.trade.flattenCount())
	round := h.steps("e1")[summaryKey(RoundFlatten)]
	require.Equal(t, store.DelistPartial, round.Status)
	require.Contains(t, round.Error, "ta-9（资金设置读不出来）")
	require.Contains(t, round.Result, "计价币不是结算币")
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)
	// 关注项会提醒：未结束的记录里有只完成一部分的步骤。
	failed, err := h.store.ListUnfinishedWithFailedSteps(context.Background())
	require.NoError(t, err)
	require.Len(t, failed, 1)

	// 没有查到持有者，但有没能判断的账户：这一轮不是“没有策略持有”。
	h2 := newHarness(t)
	h2.trade.skipped = skipped
	h2.addExecution("e2", delistAt, "ALL")
	h2.step("e2", baseTime)
	h2.step("e2", delistAt.Add(-24*time.Hour))
	round = h2.steps("e2")[summaryKey(RoundFlatten)]
	require.Equal(t, store.DelistPartial, round.Status)
	require.NotContains(t, round.Result, "没有策略持有，无需平仓")

	// 核对：没人持有，但有没能判断的账户，不能通过。
	out := h2.step("e2", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusFailed, out.Status)
	require.Contains(t, out.Error, "需要人工确认")
	require.Equal(t, store.DelistFailed, h2.steps("e2")[verifyKey()].Status)

	// 账户确认之后（不再有没能判断的），重新执行核对通过。
	h2.trade.skipped = nil
	require.Equal(t, store.StatusSucceeded, h2.step("e2", delistAt.Add(11*time.Minute)).Status)
}

// 停用或出错的账户照常列出，处理器照常当作持有者平仓（会失败重试，核对仍有持有时告警）。
func TestDisabledAccountHolderIsFlattenedLikeAnyOther(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	disabled := action.Holder{SpaceID: "crypto", LogicalAccountID: "la-off", Exchange: "binance", Quantity: "3", Status: "DISABLED"}
	h.trade.setHolders(disabled)
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorFailed, "账户已停用" }
	h.addExecution("e1", delistAt, "ALL")
	h.step("e1", baseTime)
	h.step("e1", delistAt.Add(-24*time.Hour))
	require.Equal(t, 1, h.trade.flattenCount())
	out := h.step("e1", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusFailed, out.Status, "核对仍有持有，告警")
}
