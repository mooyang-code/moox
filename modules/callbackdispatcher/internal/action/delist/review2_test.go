package delist

// C6 第 2 轮复审的回归用例。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"github.com/stretchr/testify/require"
)

// lostResponseTrade：第一次 BlockInstrument 在 Trade 上已经写入名单，但响应丢了（调用方看到超时）。
type lostResponseTrade struct {
	*fakeTrade
	once sync.Once
}

func (l *lostResponseTrade) BlockInstrument(ctx context.Context, instrument action.InstrumentRef, reason, eventID string) (action.BlockResult, error) {
	result, err := l.fakeTrade.BlockInstrument(ctx, instrument, reason, eventID)
	lost := false
	l.once.Do(func() { lost = true })
	if lost && err == nil {
		return action.BlockResult{}, context.DeadlineExceeded
	}
	return result, err
}

// Z1：演练写名单的响应丢了（Trade 已写入），重试拿到 created=false：演练仍然认得名单里自己写的那一项（原因里带着自己的执行记录 ID），
// 结束和取消（演练第 4 步）都能解除。
func TestLostBlockResponseStillAllowsDrillToUnblock(t *testing.T) {
	for _, mode := range []string{"finish", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			h.handler.Trade = &lostResponseTrade{fakeTrade: h.trade}
			delistAt := baseTime.Add(3 * time.Hour)
			h.addExecution("drill", delistAt, "PAPER_ONLY")
			out := h.step("drill", baseTime)
			require.Equal(t, store.StatusPending, out.Status)
			require.True(t, h.trade.isBlocked(), "Trade 已经写入")
			h.step("drill", baseTime.Add(time.Minute)) // 重试拿到 created=false
			if mode == "finish" {
				out = h.step("drill", delistAt.Add(11*time.Minute))
				require.Equal(t, store.StatusSucceeded, out.Status)
			} else {
				require.NoError(t, h.cancel("drill"))
			}
			require.False(t, h.trade.isBlocked(), "自己写的名单被解除")
		})
	}
}

// 写名单成功之后、存这一步之前失败（进程退出、写库出错）：调用 Trade 之前就存下了“尝试过”的一行，之后取消和结束照样能解除。
func TestBlockWrittenButStepNotSavedIsStillReleased(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(3 * time.Hour)
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	stepCtx, cancelStep := context.WithCancel(context.Background())
	defer cancelStep()
	h.handler.Trade = &cancelAfterBlockTrade{fakeTrade: h.trade, cancel: cancelStep}
	h.clock.Set(baseTime)
	_, err := h.handler.Step(stepCtx, h.execution("drill"), baseTime)
	require.Error(t, err, "写名单之后存步骤失败")
	require.True(t, h.trade.isBlocked())
	require.Equal(t, store.DelistPending, h.steps("drill")[blockKey()].Status, "调用 Trade 之前存下的“尝试过”的一行")

	h.handler.Trade = h.trade
	require.NoError(t, h.cancel("drill"))
	require.False(t, h.trade.isBlocked(), "自己写的名单被解除")
}

type cancelAfterBlockTrade struct {
	*fakeTrade
	cancel context.CancelFunc
}

func (c *cancelAfterBlockTrade) BlockInstrument(ctx context.Context, instrument action.InstrumentRef, reason, eventID string) (action.BlockResult, error) {
	result, err := c.fakeTrade.BlockInstrument(ctx, instrument, reason, eventID)
	c.cancel()
	return result, err
}

// 名单里这一项的原因带着别人的执行记录 ID（执行记录 ID 只按完整标记匹配，不会因为前缀相同而认错）。
func TestOwnerMarkerMatchesWholeExecutionID(t *testing.T) {
	h := newHarness(t)
	h.trade.blocked[foo] = "下架：binance SPOT FOOUSDT，下架时间 2026-11-11T08:00:00Z（" + blockOwnerMarker("drill-2")
	h.addExecution("drill", baseTime.Add(3*time.Hour), "PAPER_ONLY")
	h.step("drill", baseTime)
	require.NoError(t, h.cancel("drill"))
	require.True(t, h.trade.isBlocked(), "名单是 drill-2 写的，不是 drill 的")
	owned, err := h.handler.ownsListedBlock(context.Background(), store.Execution{ExecutionID: "drill-2"}, target{Instrument: foo})
	require.NoError(t, err)
	require.True(t, owned)
}

// Z6：演练结束与同标的真实下架接收并首次推进并发：真实记录未取消时名单必须还在。
func TestConcurrentDrillFinishAndRealBlock(t *testing.T) {
	for i := 0; i < 20; i++ {
		h := newHarness(t)
		delistAt := baseTime.Add(3 * time.Hour)
		h.addExecution("drill", delistAt, "PAPER_ONLY")
		h.step("drill", baseTime)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			execution := h.execution("drill")
			if _, err := h.handler.Step(context.Background(), execution, delistAt.Add(11*time.Minute)); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			h.addExecution("real", baseTime.Add(72*time.Hour), "ALL")
			if _, err := h.handler.Step(context.Background(), h.execution("real"), baseTime.Add(time.Hour)); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
		require.True(t, h.trade.isBlocked(), "第 %d 次：真实下架还在处理，名单必须在", i)
	}
}

// Z7：演练结束后解除失败时只重试解除：核对行的尝试次数不增加，零头告警不重复。
func TestDrillReleaseRetryDoesNotReverify(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(3 * time.Hour)
	h.trade.setHolders(action.Holder{SpaceID: "crypto", LogicalAccountID: "la-p", Exchange: "binance", Quantity: "0.0001", Dust: true, Paper: true})
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("drill", baseTime)
	h.trade.unblockHook = func(context.Context) error { return errors.New("Trade 超时") }
	at := delistAt.Add(11 * time.Minute)
	out := h.step("drill", at)
	require.Equal(t, store.StatusPending, out.Status)
	require.Contains(t, out.Error, "解除禁止买入失败")
	holdersCalls := h.trade.callCount("ListHolders")
	attempts := h.steps("drill")[verifyKey()].Attempts
	require.Equal(t, 1, attempts)

	for i := 1; i <= 3; i++ {
		out = h.step("drill", at.Add(time.Duration(i)*time.Minute))
		require.Equal(t, store.StatusPending, out.Status)
	}
	require.Equal(t, holdersCalls, h.trade.callCount("ListHolders"), "只重试解除，不重新查持有者")
	require.Equal(t, attempts, h.steps("drill")[verifyKey()].Attempts)

	h.trade.unblockHook = nil
	out = h.step("drill", at.Add(5*time.Minute))
	require.Equal(t, store.StatusSucceeded, out.Status)
	require.False(t, h.trade.isBlocked())
	require.Empty(t, h.steps("drill")[blockKey()].ReleaseVerdict, "解除成功后清除待重试标记")
}

// 核对失败的演练已经解除了名单：之后重新执行时，名单里没有这个标的，记录自己停下（演练已经结束），不会被“待重试解除”的标记卡住。
func TestFailedDrillRetryStopsBecauseBlockWasReleased(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(3 * time.Hour)
	h.trade.setHolders(paper("crypto", "la-p"))
	h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorPartial, "拒单" }
	h.addExecution("drill", delistAt, "PAPER_ONLY")
	h.step("drill", baseTime)
	h.step("drill", baseTime.Add(31*time.Second))
	require.Equal(t, store.StatusFailed, h.step("drill", delistAt.Add(11*time.Minute)).Status)
	require.False(t, h.trade.isBlocked())
	require.Empty(t, h.steps("drill")[blockKey()].ReleaseVerdict, "解除成功，没有待重试的标记")
	out := h.step("drill", delistAt.Add(12*time.Minute))
	require.Equal(t, store.StatusCanceled, out.Status)
	require.Contains(t, out.Error, "手工解除")
}

// 没查到持有者但有没能判断的账户：同样发 delist_detected。
func TestDelistDetectedWhenOnlySkippedAccounts(t *testing.T) {
	h := newHarness(t)
	h.trade.skipped = []action.SkippedAccount{{TradingAccountID: "ta-9", Reason: "资金设置读不出来"}}
	h.addExecution("e1", baseTime.Add(48*time.Hour), "ALL")
	h.step("e1", baseTime)
	notes := telemetry.Recent(telemetry.AttentionDelistDetected, baseTime, time.Minute)
	require.Contains(t, notes, "binance:SPOT:FOOUSDT 将于 2026-11-09 08:00 UTC 下架，1 个账户没能判断是否持有，请人工确认", "进程里的关注项记录是共享的，只看这一条")
}

// 第 3 轮复审小 4：演练核对已有结论、解除其实已在 Trade 上生效只是响应丢了：下一次推进只重试解除（名单里已经没有这一项也算解除完成），
// 返回已存下的核对结论，不会被“名单里没有这个标的”置成已取消，也不丢失败结论。
func TestDrillReleaseLostResponseKeepsVerdict(t *testing.T) {
	for _, verdict := range []string{"succeeded", "failed"} {
		t.Run(verdict, func(t *testing.T) {
			h := newHarness(t)
			delistAt := baseTime.Add(3 * time.Hour)
			if verdict == "failed" {
				h.trade.setHolders(paper("crypto", "la-p"))
				h.trade.finalFor = func(action.FlattenRequest) (string, string) { return action.OperatorPartial, "拒单" }
			}
			h.addExecution("drill", delistAt, "PAPER_ONLY")
			h.step("drill", baseTime)
			h.step("drill", baseTime.Add(31*time.Second))
			lost := true
			h.trade.unblockHook = func(context.Context) error {
				if lost {
					lost = false
					h.trade.mu.Lock()
					delete(h.trade.blocked, foo)
					h.trade.mu.Unlock()
					return context.DeadlineExceeded
				}
				return nil
			}
			at := delistAt.Add(11 * time.Minute)
			out := h.step("drill", at)
			require.Equal(t, store.StatusPending, out.Status)
			require.False(t, h.trade.isBlocked())
			require.NotEmpty(t, h.steps("drill")[blockKey()].ReleaseVerdict, "核对结论记在禁止买入那一行")

			out = h.step("drill", at.Add(time.Minute))
			want := store.StatusSucceeded
			if verdict == "failed" {
				want = store.StatusFailed
				require.Contains(t, out.Error, "仍有 1 个组合持有")
			}
			require.Equal(t, want, out.Status, "保留核对结论")
			require.Empty(t, h.steps("drill")[blockKey()].ReleaseVerdict)
			require.Equal(t, 1, h.steps("drill")[verifyKey()].Attempts, "没有重新核对")
		})
	}
}

// 未知标的的演练：结束时解除失败，下一次推进直接重试解除并结束，不重走整条时间线。
func TestUnknownInstrumentDrillReleaseRetryFinishesDirectly(t *testing.T) {
	h := newHarness(t)
	h.trade.unknown = true
	h.addExecution("drill", baseTime.Add(48*time.Hour), "PAPER_ONLY")
	h.trade.unblockHook = func(context.Context) error { return errors.New("Trade 超时") }
	out := h.step("drill", baseTime)
	require.Equal(t, store.StatusPending, out.Status)
	require.Equal(t, baseTime.Add(time.Minute), out.NextRunAt)
	h.trade.unblockHook = nil
	out = h.step("drill", baseTime.Add(time.Minute))
	require.Equal(t, store.StatusSucceeded, out.Status, "直接重试解除并结束")
	require.False(t, h.trade.isBlocked())
	require.Zero(t, h.trade.callCount("ListHolders"), "没有重走时间线")
}
