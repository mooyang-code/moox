package target

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// submittedChild 建一笔本目标已提交的子单（合约，account-a），用来占用每小时额度。
func (f *targetFixture) submittedChild(t *testing.T, orderID, state, quantity, filled, price string, submittedAt time.Time) {
	t.Helper()
	at := submittedAt.UnixMilli()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: orderID, Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "BUY", Quantity: quantity, ReferencePrice: price, AveragePrice: price,
		FilledQuantity: filled, OwnerType: "TARGET", OwnerID: "target-current", State: state, SubmittedAt: at, Version: 1,
		FinishedAt: map[bool]int64{true: at, false: 0}[state == "FILLED" || state == "CANCELED"],
	})
}

const capSettings = `{"order_mode":"MARKET","min_notional_swap":"0","hourly_cap_swap":{"BTC-USDT-SWAP":"1000"}}`

func TestHourlyCapClipsTheSliceToTheRemainingAllowance(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "10", f.orders.specs[0].Quantity.String(), "额度 1000 ÷ 价格 100 = 10")
}

func TestHourlyCapCountsOnlyWhatWasActuallySubmittedThisHour(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	hour := f.now.Truncate(time.Hour)
	// 本小时已成交 9 个（900）：剩余额度 100，只下 1 个。
	f.submittedChild(t, "used", "FILLED", "9", "9", "100", hour.Add(time.Second))
	// 这几笔都不占额度：只在本地丢弃的（没提交过）、被拒绝的、上一个小时的。
	f.submittedChild(t, "discarded", "CANCELED", "50", "0", "100", time.UnixMilli(0))
	f.submittedChild(t, "rejected", "REJECTED", "50", "0", "100", hour.Add(2*time.Second))
	f.submittedChild(t, "last-hour", "FILLED", "50", "50", "100", hour.Add(-time.Minute))
	// 提交了但一笔没成交就结束的单也不占额度（结束的单只算实际成交额）。
	f.submittedChild(t, "unfilled", "CANCELED", "50", "0", "100", hour.Add(3*time.Second))
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "1", f.orders.specs[0].Quantity.String())
}

func TestHourlyCapCountsInFlightOrdersAtFullSize(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	// 还没结束的单按全部数量算，不能等它成交了才扣额度。
	f.submittedChild(t, "open", "FILLED", "9", "9", "100", f.now.Truncate(time.Hour).Add(time.Second))
	rows, err := f.store.ListSubmittedTargetOrders(context.Background(), "space-1", "logical-1", 0, f.now.Add(time.Hour).UnixMilli())
	require.NoError(t, err)
	require.Equal(t, "900", submittedNotional(rows[0]).String())
	rows[0].State, rows[0].FilledQuantity = "OPEN", "1"
	require.Equal(t, "900", submittedNotional(rows[0]).String(), "OPEN 的单按全部数量 9 × 100")
}

// 额度用完：这个标的顺延到下一个整点，目标定为已收敛并设好唤醒时间，不再每秒重算。
func TestHourlyCapExhaustedDefersToNextHour(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.submittedChild(t, "used", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusConverged, result.Status, "全是顺延：按计划 4.4 定为已收敛，不是阻塞")
	require.Greater(t, result.RetryAfter, time.Duration(0))
	require.LessOrEqual(t, result.RetryAfter, time.Hour+time.Second)
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 1)
	require.Equal(t, "BTC-USDT-SWAP", target.BlockedTargets[0].InstrumentID)
	require.Equal(t, reasonHourlyCapDeferred, target.BlockedTargets[0].Reason, "原因写进 blocked_targets 让界面看得到，文案不带具体时刻")
	require.True(t, target.BlockedTargets[0].Deferred, "标记为顺延，界面用中性色显示")
	require.NotContains(t, target.BlockedTargets[0].Reason, ":")
	require.Equal(t, nextHourRetryDelay(f.now).Round(time.Minute), result.RetryAfter.Round(time.Minute))
}

// 下一个整点继续下顺延的部分：新的一小时里已用额度为 0。
func TestHourlyCapAllowanceResetsAtTheNextHour(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.submittedChild(t, "used", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	f.converge(t)
	require.Empty(t, f.orders.specs)

	f.now = f.now.Truncate(time.Hour).Add(time.Hour + 2*time.Second)
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "10", f.orders.specs[0].Quantity.String())
}

// 剩下的额度不够下一单（低于配置的最小下单额）也算顺延，不是“下不了单”的阻塞。
func TestHourlyCapRemainderBelowMinimumIsDeferredNotBlocked(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"50","hourly_cap_swap":{"*":"1000"}}`)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.submittedChild(t, "used", "FILLED", "9.7", "9.7", "100", f.now.Truncate(time.Hour).Add(time.Second))
	result := f.converge(t)
	require.Empty(t, f.orders.specs)
	require.Equal(t, StatusConverged, result.Status, "剩余额度 30 低于最小下单额 50：顺延，不算阻塞")
	require.Greater(t, result.RetryAfter, time.Duration(0))
}

// 顺延的标的不卡后面的阶段：合约加仓被每小时上限顺延时，现货买入照常下。
func TestHourlyCapDeferredLaneDoesNotStallLaterStages(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","min_notional_swap":"0","hourly_cap_swap":{"*":"1000"}}`)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
	})
	f.submittedChild(t, "used", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	result := f.converge(t)
	require.Equal(t, []string{"BTC-USDT-SPOT:BUY"}, placedInstruments(f))
	require.Equal(t, StatusConverging, result.Status)
}

func TestHourlyCapIsOnlyQueriedWhenConfigured(t *testing.T) {
	f := swapPolicyFixture(t, legacyOrderSettings)
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	executor := f.executor()
	env := executor.newLaneEnv(context.Background(), "space-1", "logical-1", mustSettings(t, legacyOrderSettings), nil, nil)
	require.False(t, env.settings.HasHourlyCaps())
	require.False(t, env.hourlyLoaded)
	_, clipped, err := env.applyHourlyCap(context.Background(), laneAction{instrument: store.InstrumentRecord{MarketType: string(exchange.MarketTypeSwap), InstrumentID: "BTC-USDT-SWAP", ExchangeQuantityStep: "1"}}, mustDecimal("5"), mustDecimal("100"))
	require.NoError(t, err)
	require.False(t, clipped)
	require.False(t, env.hourlyLoaded, "没配置每小时上限就不查订单表")
}

// 上限只限制加仓和建仓：本小时先买满额度后，目标改成 0 的清仓、减到更小目标的减仓照常下，金额照常计入汇总。
func TestHourlyCapNeverBlocksReductions(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS","hourly_cap_swap":{"BTC-USDT-SWAP":"1000"}}`)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "10")
	f.target(t, nil)
	// 本小时已经买了 10 个（1000）：额度用满。
	f.submittedChild(t, "bought", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	result := f.converge(t)
	require.Len(t, f.orders.specs, 1, "清仓不受上限限制")
	require.Equal(t, exchange.SideSell, f.orders.specs[0].Side)
	require.Equal(t, "10", f.orders.specs[0].Quantity.String(), "整笔清掉，不被额度收窄")
	require.Equal(t, StatusConverging, result.Status)

	reducing := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","rebalance_threshold_mode":"ALWAYS","hourly_cap_swap":{"BTC-USDT-SWAP":"1000"}}`)
	reducing.now = time.Now().UTC()
	reducing.position(t, "account-a", "BTCUSDT", "10")
	reducing.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "4"}})
	reducing.submittedChild(t, "bought", "FILLED", "10", "10", "100", reducing.now.Truncate(time.Hour).Add(time.Second))
	reducing.converge(t)
	require.Len(t, reducing.orders.specs, 1, "减仓不受上限限制")
	require.Equal(t, "6", reducing.orders.specs[0].Quantity.String())
}

// 减仓的金额照常计入汇总：本小时的买入加卖出都占额度，之后的加仓只能用剩下的。
func TestHourlyCapStillCountsReductionNotional(t *testing.T) {
	f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","hourly_cap_swap":{"BTC-USDT-SWAP":"1000"}}`)
	f.now = time.Now().UTC()
	hour := f.now.Truncate(time.Hour)
	f.submittedChild(t, "sold", "FILLED", "4", "4", "100", hour.Add(time.Second))
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.converge(t)
	require.Len(t, f.orders.specs, 1)
	require.Equal(t, "6", f.orders.specs[0].Quantity.String(), "额度 1000 − 已提交 400 = 600")
}

// 有真实阻塞又有顺延：目标定为 BLOCKED，真实原因排在前面（监控关注项只取第一条），顺延的在后。
func TestHourlyCapDeferralAfterARealBlockKeepsTheRealReasonFirst(t *testing.T) {
	f := newMixedExecutorFixture(t)
	f.setOrderSettings(t, `{"order_mode":"MARKET","min_notional_spot":"0","min_notional_swap":"0","hourly_cap_swap":{"*":"1000"}}`)
	f.now = time.Now().UTC()
	// BTC 现货买单金额低于交易所最小名义金额，下不了单（真实阻塞）；BTC 合约额度用满（顺延）。
	require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_instruments SET c_min_notional = '100000' WHERE c_instrument_id = 'BTC-USDT-SPOT'").Error)
	f.target(t, []store.InstrumentTarget{
		{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"},
		{InstrumentID: "BTC-USDT-SPOT", Quantity: "1"},
	})
	f.submittedChild(t, "used", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	result := f.converge(t)
	require.Equal(t, StatusBlocked, result.Status)
	require.Greater(t, result.RetryAfter, time.Duration(0), "顺延的唤醒时间照常")
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, target.BlockedTargets, 2)
	require.Equal(t, "BTC-USDT-SPOT", target.BlockedTargets[0].InstrumentID, "真实阻塞排前面")
	require.Equal(t, reasonHourlyCapDeferred, target.BlockedTargets[1].Reason)
	require.False(t, target.BlockedTargets[0].Deferred, "真实阻塞不带顺延标记")
	require.True(t, target.BlockedTargets[1].Deferred)
}

// 顺延期间过了整点：定时唤醒后自动恢复，下顺延的部分，blocked_targets 里的顺延项清掉。
func TestHourlyCapDeferralRecoversAfterTheHourAndClearsTheList(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.submittedChild(t, "used", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	first := f.converge(t)
	require.Equal(t, StatusConverged, first.Status)
	deferred, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Len(t, deferred.BlockedTargets, 1)

	// 定时器按 RetryAfter 在整点之后唤醒。
	f.now = f.now.Add(first.RetryAfter)
	resumed := f.converge(t)
	require.Equal(t, StatusConverging, resumed.Status)
	require.Equal(t, "place", resumed.Action)
	require.Len(t, f.orders.specs, 1)
	after, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Empty(t, after.BlockedTargets)
}

// 顺延期间来了新目标：额度按组合累计，不因为换了目标就重新给；新目标是减仓时照常下，不受顺延影响。
func TestHourlyCapDeferralSurvivesANewTargetAndNeverBlocksItsReductions(t *testing.T) {
	f := swapPolicyFixture(t, capSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.submittedChild(t, "used", "FILLED", "10", "10", "100", f.now.Truncate(time.Hour).Add(time.Second))
	require.Equal(t, StatusConverged, f.converge(t).Status)
	require.Empty(t, f.orders.specs)

	// 新目标（还是加仓）：额度仍是本小时已用满，继续顺延，blocked_targets 换成新目标的。
	_, accepted, err := f.store.AcceptLogicalAccountTarget(context.Background(), store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1", TargetID: "target-next", InstanceID: "runner-1", SessionID: "session-1", StrategyID: "strategy-1",
		BarEndTime: f.now.UnixMilli() + 1, EffectiveAt: f.now.UnixMilli() + 1, ValidUntil: fixtureValidUntil(f.now),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "200"}}, Status: StatusPending, AcceptedAt: f.now.UnixMilli() + 1,
	})
	require.NoError(t, err)
	require.True(t, accepted)
	f.now = f.now.Add(time.Second)
	result := f.converge(t)
	require.Equal(t, StatusConverged, result.Status)
	require.Empty(t, f.orders.specs, "换了目标额度也不重新给")
	target, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "target-next", target.TargetID)
	require.Len(t, target.BlockedTargets, 1)
	require.True(t, target.BlockedTargets[0].Deferred)
}
