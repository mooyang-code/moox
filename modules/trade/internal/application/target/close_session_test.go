package target

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 关闭策略会话（批 3 的 3.6）之后执行器怎么收尾：关闭只推进组合的 c_closed_through、缩短目标行的有效期，
// 撤单与置为过期都走执行器现有的过期路径。这里直接调库里的关闭（logicalaccount.Service.CloseTargetSession 在组合锁里做的就是这一步），
// 提交之后的唤醒见 logicalaccount 与 runtime 包的测试。

// closeSessionAt 在 now 时刻关闭 session-1（开始时刻按 sessionStartedAt 取），返回目标行的有效期是否被改。
func (f *targetFixture) closeSessionAt(t *testing.T, sessionID string, now time.Time) bool {
	t.Helper()
	var shortened bool
	require.NoError(t, f.store.Transaction(context.Background(), func(tx *store.Tx) error {
		var err error
		_, shortened, err = tx.CloseTargetSession("space-1", "logical-1", sessionStartedAt(sessionID), now.UnixMilli())
		return err
	}))
	return shortened
}

// convergedRestingTarget 搭一份已收敛、带一笔挂单的目标：挂单覆盖了整笔差额，收敛结果是 CONVERGED，
// 下一次唤醒按旧有效期算（还有一个多小时）。
func convergedRestingTarget(t *testing.T) *targetFixture {
	t.Helper()
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now)
	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, result.Status)
	require.Greater(t, result.RetryAfter, 30*time.Minute, "不关闭时，挂单要等旧有效期到点才撤")
	require.Empty(t, f.orders.canceled)
	return f
}

// 关闭后目标行还在、状态变为过期、挂单被撤：已收敛并带挂单的目标被关闭后，紧接着的一次收敛（关闭提交后唤醒的那一次）
// 就走过期路径撤掉挂单，不等旧有效期算出来的定时器。
func TestClosedSessionTargetExpiresAndCancelsRestingOrder(t *testing.T) {
	f := convergedRestingTarget(t)
	f.now = f.now.Add(time.Second)
	require.True(t, f.closeSessionAt(t, "session-1", f.now), "关闭要缩短这份目标的有效期")
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, f.now.UnixMilli(), target.ValidUntil, "有效期取“现在”（已晚于 bar_end_time 加 1 毫秒）")

	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")

	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Equal(t, []string{"resting-1"}, f.orders.canceled, "挂单被撤")
	require.Empty(t, f.orders.specs, "不再下新单")
	target, err = f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err, "关闭不删目标行")
	require.Equal(t, "target-current", target.TargetID)
	require.Equal(t, StatusExpired, target.Status)
}

// 撤单失败后由过期路径重试：目标保持原状、按 expiryRetryDelay 再来一次，撤下来之后才置为过期。
func TestClosedSessionCancelFailureRetriesThroughExpiryPath(t *testing.T) {
	f := convergedRestingTarget(t)
	f.now = f.now.Add(time.Second)
	f.closeSessionAt(t, "session-1", f.now)
	f.orders.cancelErr = errors.New("交易所暂时不可用")

	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")

	require.Error(t, err)
	require.Equal(t, expiryRetryDelay, result.RetryAfter, "撤不下来时过期路径自己按间隔重试")
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, target.Status, "撤单失败时目标保持原状")

	f.orders.cancelErr = nil
	f.orders.canceled = nil
	f.now = f.now.Add(expiryRetryDelay)
	result, err = f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.Equal(t, []string{"resting-1"}, f.orders.canceled)
	target, err = f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, target.Status)
}

// 目标的 bar_end_time 还没到时关闭：有效期取 bar_end_time 加 1 毫秒（不能早于 bar_end_time，否则违反表约束、
// 执行器也会当成无效目标绕开过期路径）；到点之前目标照常等生效，到点之后照常过期并撤掉上一份目标留下的挂单。
func TestClosedSessionBeforeBarEndExpiresAtBarEndPlusOne(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	barEnd := f.now.Add(10 * time.Minute)
	_, accepted, err := f.store.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-current",
		InstanceID: "runner-1", SessionID: "session-1", SessionStartedAt: sessionStartedAt("session-1"), StrategyID: "strategy-1",
		BarEndTime: barEnd.UnixMilli(), ValidUntil: barEnd.Add(time.Hour).UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}}, Status: StatusPending, AcceptedAt: f.now.UnixMilli(),
	})
	require.NoError(t, err)
	require.True(t, accepted)
	// 上一份目标留下的挂单：新目标生效后本该在第 1 阶段撤掉。
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "resting-old", TradingAccountID: "account-a", LogicalAccountID: "logical-1", InstanceID: "runner-1",
		ClientOrderID: "resting-old", ExchangeOrderID: "exchange-resting-old", Exchange: "binance", MarketType: "swap", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "10", LimitPrice: strPtr("99.9"), ReferencePrice: "100",
		FilledQuantity: "0", OwnerType: "TARGET", OwnerID: "target-old", State: "OPEN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})

	require.True(t, f.closeSessionAt(t, "session-1", f.now))
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, barEnd.UnixMilli()+1, target.ValidUntil, "有效期取 bar_end_time 加 1 毫秒")

	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusPaused, result.Status, "还没到 bar_end_time，照常等生效")
	require.Empty(t, f.orders.canceled)
	require.Empty(t, f.orders.specs)

	f.now = time.UnixMilli(barEnd.UnixMilli() + 1).UTC()
	result, err = f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status, "到点后照常过期")
	require.Equal(t, []string{"resting-old"}, f.orders.canceled, "过期路径撤掉组合里还没结束的目标挂单")
	require.Empty(t, f.orders.specs)
}
