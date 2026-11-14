package target

import (
	"context"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// handOverSession 模拟策略换实例：旧会话释放并删掉目标，新实例认领后发来新目标。
func (f *targetFixture) handOverSession(t *testing.T, quantity string) {
	t.Helper()
	ctx := context.Background()
	logical, err := f.store.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.ReleaseLogicalAccountSession("space-1", "logical-1", "runner-1", "session-1", logical.AuthFence); err != nil {
			return err
		}
		return tx.DeleteLogicalAccountTarget("space-1", "logical-1")
	}))
	logical, err = f.store.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.NoError(t, f.store.Transaction(ctx, func(tx *store.Tx) error {
		_, _, err := tx.ClaimLogicalAccountSession("space-1", "logical-1", "runner-2", "session-2", logical.AuthFence)
		return err
	}))
	_, accepted, err := f.store.AcceptLogicalAccountTarget(ctx, store.LogicalAccountTargetRecord{
		SpaceID: "space-1", LogicalAccountID: "logical-1",
		TargetID: "target-next", InstanceID: "runner-2", SessionID: "session-2", StrategyID: "strategy-1",
		BarEndTime: f.now.UnixMilli(), ValidUntil: time.Now().Add(time.Hour).UnixMilli(),
		Targets: []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: quantity}}, Status: StatusPending, AcceptedAt: f.now.UnixMilli(),
	})
	require.NoError(t, err)
	require.True(t, accepted)
}

// 换实例接管时上一个实例的挂单还在簿上（释放时没撤下来）：新目标按第 1 阶段撤掉它，不暂停组合。
func TestNewInstanceTargetStopsPreviousInstanceRestingOrder(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
	f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now)
	executor := f.restingExecutor()
	first, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverged, first.Status)

	f.handOverSession(t, "120")
	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "stop", result.Action)
	require.Equal(t, []string{"resting-1"}, f.orders.canceled)
	logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "ACTIVE", logical.AutomationState)
}

// ReleaseTargetOrders 不分哪一份目标：挂单撤掉，待提交的丢弃，市价单不动；限价 GTC 单结果未知时先解析，解析不出就报错（调用方重试）。
func TestReleaseTargetOrdersCoversEveryTarget(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now)
	base := store.OrderRecord{
		SpaceID: "space-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "TARGET", OwnerID: "target-old", Version: 1,
	}
	pending := base
	pending.OrderID, pending.ClientOrderID, pending.OrderType, pending.State = "pending-old", "pending-old", "MARKET", "PENDING"
	f.order(t, pending)
	market := base
	market.OrderID, market.ClientOrderID, market.OrderType, market.State, market.SubmittedAt = "market-old", "market-old", "MARKET", "OPEN", f.now.UnixMilli()
	f.order(t, market)

	executor := f.restingExecutor()
	require.NoError(t, executor.ReleaseTargetOrders(context.Background(), "space-1", "logical-1"))
	require.Equal(t, []string{"resting-1"}, f.orders.canceled)
	require.Equal(t, []string{"pending-old"}, f.orders.discarded)

	unknown := base
	unknown.OrderID, unknown.ClientOrderID, unknown.OrderType, unknown.TimeInForce = "gtc-unknown", "gtc-unknown", "LIMIT", "GTC"
	unknown.LimitPrice, unknown.State, unknown.SubmittedAt = strPtr("99"), "SUBMIT_UNKNOWN", f.now.UnixMilli()
	f.order(t, unknown)
	err := executor.ReleaseTargetOrders(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	require.Contains(t, f.orders.resolved, "gtc-unknown")
}

// recoveringOrders 能确认撤单结果：把撤单结果未知的单记成已撤销。
type recoveringOrders struct{ *targetOrderServiceStub }

func (o recoveringOrders) RecoverCancel(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	record, err := o.store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	expected := record.Version
	record.State, record.Version = string(orderdomain.Canceled), record.Version+1
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Canceled}, o.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

// 上一次撤单结果未知的挂单（释放重试时会遇到）：先确认撤单结果，确认不了就报错，不能当作已经撤掉、放行释放。
func TestReleaseTargetOrdersConfirmsUnknownCancel(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "cancel-unknown", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "cancel-unknown", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "10", LimitPrice: strPtr("99.9"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: "target-current", State: "CANCEL_UNKNOWN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
	executor := f.restingExecutor()
	require.ErrorIs(t, executor.ReleaseTargetOrders(context.Background(), "space-1", "logical-1"), errTargetOrderUnresolved,
		"订单服务确认不了撤单结果")

	f.orders.store = f.store
	executor.Orders = recoveringOrders{f.orders}
	require.NoError(t, executor.ReleaseTargetOrders(context.Background(), "space-1", "logical-1"))
	order, err := f.store.GetOrder(context.Background(), "space-1", "cancel-unknown")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", order.State)
}

// 挂单到期被撤后，同一目标里这个标的下一轮改吃单，不再挂单（避免撤了重挂一直循环）。
func TestRestingTTLExpiryFallsBackToTaking(t *testing.T) {
	for _, ratio := range []string{"1", "0.5"} {
		f := swapPolicyFixture(t, `{"order_mode":"MARKET","min_notional_swap":"0","resting_ratio":"`+ratio+`","resting_ttl_seconds":600}`)
		f.now = time.Now().UTC()
		f.position(t, "account-a", "BTCUSDT", "100")
		f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "110"}})
		f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now.Add(-700*time.Second))
		executor := f.restingExecutor()
		_, err := executor.Converge(context.Background(), "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, []string{"resting-1"}, f.orders.canceled)
		require.NoError(t, f.store.DBForTest().Exec("UPDATE t_trade_orders SET c_state = 'CANCELED', c_finished_at = ? WHERE c_order_id = 'resting-1'", f.now.UnixMilli()).Error)
		_, err = executor.Converge(context.Background(), "space-1", "logical-1")
		require.NoError(t, err)
		require.Len(t, f.orders.specs, 1, ratio)
		require.Equal(t, exchange.OrderTypeMarket, f.orders.specs[0].Type, ratio)
	}
}

// 新目标在第 1 阶段之前就过期（成员一直未就绪）：过期时上一份目标留下的挂单也撤掉。
func TestExpiredNewTargetStopsPreviousRestingOrders(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.position(t, "account-a", "BTCUSDT", "100")
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: "old-resting", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		ClientOrderID: "old-resting", Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "10", LimitPrice: strPtr("99.9"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: "target-old", State: "OPEN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "100"}})
	f.setReady(t, "account-b", false)
	executor := f.restingExecutor()
	paused, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusPaused, paused.Status)
	require.Empty(t, f.orders.canceled)

	f.now = f.now.Add(2 * time.Hour)
	expired, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, expired.Status)
	require.Equal(t, []string{"old-resting"}, f.orders.canceled)
}

// operatorChild 在成员账户上放一笔人工动作的子单。
func (f *targetFixture) operatorChild(t *testing.T, orderID, actionID, state string) {
	t.Helper()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-a", LogicalAccountID: "logical-1",
		ClientOrderID: orderID, Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "MARKET", Side: "SELL", Quantity: "5", ReferencePrice: "100",
		OwnerType: "OPERATOR", OwnerID: actionID, State: state, SubmittedAt: 1, Version: 1,
	})
}

func (f *targetFixture) operatorAction(t *testing.T, actionID, actionType, status string) {
	t.Helper()
	_, _, err := f.store.CreateOperatorAction(context.Background(), store.OperatorActionRecord{
		SpaceID: "space-1", ActionID: actionID, LogicalAccountID: "logical-1", ActionType: actionType,
		Reason: "测试", RequestJSON: `{"logical_account_id":"logical-1","instrument_id":"BTC-USDT-SWAP","exchange":"BINANCE"}`,
		Status: status,
	})
	require.NoError(t, err)
}

// 清仓一族的动作结束后还留着没结束的子单（例如挂单中）：不算冲突，不暂停组合；人工下单的单照样算冲突。
func TestFinishedFlattenChildrenDoNotPauseButManualOrdersDo(t *testing.T) {
	for _, c := range []struct {
		actionType string
		status     string
		pauses     bool
	}{
		{"DELIST_FLATTEN", "PARTIAL", false},
		{"FLATTEN", "PARTIAL", false},
		{"REDUCE", "FAILED", false},
		{"MANUAL_ORDER", "COMPLETED", true},
	} {
		t.Run(c.actionType, func(t *testing.T) {
			f := newMixedExecutorFixture(t)
			f.setSwapPosition("BTCUSDT", "5")
			f.block(t, "SWAP", "BTCUSDT")
			f.operatorAction(t, "action-1", c.actionType, c.status)
			f.operatorChild(t, "operator-child", "action-1", "OPEN")
			f.target(t, []store.InstrumentTarget{{InstrumentID: "ETH-USDT-SPOT", Quantity: "12"}})

			result := f.converged(t)
			logical, err := f.store.GetLogicalAccount(context.Background(), "space-1", "logical-1")
			require.NoError(t, err)
			if c.pauses {
				require.Equal(t, StatusPaused, result.Status)
				require.Equal(t, "PAUSED", logical.AutomationState)
				return
			}
			require.NotEqual(t, StatusPaused, result.Status, logical.PauseReason)
			require.Equal(t, "ACTIVE", logical.AutomationState)
		})
	}
}

// 查无此单退回待提交、随后丢弃的伪市价子单从没发到交易所，不计入尝试次数。
func TestPseudoMarketAttemptsSkipNeverSentChildren(t *testing.T) {
	sent := store.TargetChildOrder{OrderType: "LIMIT", TimeInForce: "IOC", Quantity: "5", FilledQuantity: "0", State: "EXPIRED", ExchangeOrderID: "1"}
	phantom := store.TargetChildOrder{OrderType: "LIMIT", TimeInForce: "IOC", Quantity: "5", FilledQuantity: "0", State: "CANCELED"}
	rejected := store.TargetChildOrder{OrderType: "LIMIT", TimeInForce: "IOC", Quantity: "5", FilledQuantity: "0", State: "REJECTED"}
	require.Equal(t, 1, pseudoMarketAttempts([]store.TargetChildOrder{sent, phantom}))
	require.Equal(t, 2, pseudoMarketAttempts([]store.TargetChildOrder{sent, phantom, rejected}), "交易所拒绝的单算一次尝试")
}

// resolvingOrders 解析结果未知的单：toPending 里的按“查无此单、退回待提交”改库，其余保持结果未知。
type resolvingOrders struct {
	*targetOrderServiceStub
	toPending map[string]bool
}

func (o resolvingOrders) ResolveUnknown(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	o.resolved = append(o.resolved, orderID)
	if !o.toPending[orderID] {
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.SubmitUnknown}, nil
	}
	record, err := o.store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	expected := record.Version
	record.State, record.Version = string(orderdomain.Pending), record.Version+1
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Pending}, o.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

// 释放时市价、IOC 单结果未知也要解析一次：退回待提交的丢弃（否则目标删掉后没人管）；仍解析不出来的不挡释放，它们自己会结束。
func TestReleaseTargetOrdersResolvesEveryUnknownOrder(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	base := store.OrderRecord{
		SpaceID: "space-1", TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: "runner-1",
		Exchange: "BINANCE", MarketType: "SWAP", InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "TARGET", OwnerID: "target-old",
		State: "SUBMIT_UNKNOWN", SubmittedAt: f.now.UnixMilli(), Version: 1,
	}
	market := base
	market.OrderID, market.ClientOrderID, market.OrderType = "market-unknown", "market-unknown", "MARKET"
	f.order(t, market)
	ioc := base
	ioc.OrderID, ioc.ClientOrderID, ioc.OrderType, ioc.TimeInForce, ioc.LimitPrice = "ioc-unknown", "ioc-unknown", "LIMIT", "IOC", strPtr("99")
	f.order(t, ioc)
	f.orders.store = f.store
	executor := f.restingExecutor()
	executor.Orders = resolvingOrders{targetOrderServiceStub: f.orders, toPending: map[string]bool{"market-unknown": true}}

	require.NoError(t, executor.ReleaseTargetOrders(context.Background(), "space-1", "logical-1"), "IOC 仍解析不出来也不挡释放")
	require.ElementsMatch(t, []string{"market-unknown", "ioc-unknown"}, f.orders.resolved)
	require.Equal(t, []string{"market-unknown"}, f.orders.discarded, "退回待提交的丢弃")
	order, err := f.store.GetOrder(context.Background(), "space-1", "market-unknown")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", order.State)
}

// deferredSyncOrders 支持撤单后不立刻同步，记下每次同步的账户。
type deferredSyncOrders struct {
	*targetOrderServiceStub
	syncs *[]string
}

func (o deferredSyncOrders) Cancel(context.Context, string, string) (orderdomain.Order, error) {
	panic("释放时应该用不带同步的撤单")
}

func (o deferredSyncOrders) CancelWithoutSync(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	return o.targetOrderServiceStub.Cancel(ctx, spaceID, orderID)
}

func (o deferredSyncOrders) SyncAfterCancel(_ context.Context, tradingAccountID string) error {
	*o.syncs = append(*o.syncs, tradingAccountID)
	return nil
}

// 释放时撤 N 笔挂单只在最后按账户同步一次，不是每笔撤单都做一次全量同步。
func TestReleaseTargetOrdersSyncsOncePerAccount(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.restingOrder(t, "resting-1", "BUY", "10", "0", f.now)
	f.restingOrder(t, "resting-2", "BUY", "5", "0", f.now)
	f.restingOrder(t, "resting-3", "BUY", "5", "0", f.now)
	var syncs []string
	executor := f.restingExecutor()
	executor.Orders = deferredSyncOrders{targetOrderServiceStub: f.orders, syncs: &syncs}

	require.NoError(t, executor.ReleaseTargetOrders(context.Background(), "space-1", "logical-1"))
	require.ElementsMatch(t, []string{"resting-1", "resting-2", "resting-3"}, f.orders.canceled)
	require.Equal(t, []string{"account-a"}, syncs)
}
