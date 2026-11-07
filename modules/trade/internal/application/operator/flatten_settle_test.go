package operator

import (
	"context"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

// keepPendingOrders 让提交后的子单停在待提交（模拟账户未就绪时提交没发出去），丢弃照常记为已撤销；
// unknown 里的子单解析后仍是结果未知（模拟未知窗口还没过）。
type keepPendingOrders struct {
	*operatorOrderStub
	keep    map[string]bool
	unknown map[string]bool
}

func (o keepPendingOrders) Submit(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	if o.keep[orderID] {
		o.fixture.trace = append(o.fixture.trace, "submit:"+orderID)
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Pending}, nil
	}
	return o.operatorOrderStub.Submit(ctx, spaceID, orderID)
}

func (o keepPendingOrders) ResolveUnknown(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	if o.unknown[orderID] {
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.SubmitUnknown}, nil
	}
	return o.operatorOrderStub.ResolveUnknown(ctx, spaceID, orderID)
}

func childState(t *testing.T, fixture *operatorFixture, orderID string) string {
	t.Helper()
	child, err := fixture.store.GetOrder(context.Background(), "space-1", orderID)
	require.NoError(t, err)
	return child.State
}

// 下架清仓到截止时间记 PARTIAL 时，本动作还没提交出去的子单直接丢弃，不留给执行器当冲突。
func TestDelistDeadlineDiscardsOwnPendingChild(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-pending", "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(time.Hour).UnixMilli()
	fixture.orders.nextID = "child-pending"
	orders := keepPendingOrders{operatorOrderStub: fixture.orders, keep: map[string]bool{"child-pending": true}}
	service := fixture.service()
	service.Orders = orders

	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	require.Equal(t, "PENDING", childState(t, fixture, "child-pending"))

	// 截止时间过了：工作线程用同一个动作号恢复。
	fixture.now = fixture.now.Add(2 * time.Hour)
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-pending")
	require.NoError(t, err)
	require.NoError(t, service.ResumeOperatorAction(context.Background(), stored))

	stored, err = fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-pending")
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", stored.Status)
	require.Equal(t, "CANCELED", childState(t, fixture, "child-pending"), "待提交的子单丢弃，从没发到交易所")
}

// 子单结果未知、解析不出来时动作先保持运行（记下要结束成 PARTIAL），之后的恢复只收拾不下单，解析清楚后才记 PARTIAL。
func TestDelistDeadlineWaitsForUnknownChildThenFinishes(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-unknown", "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(time.Hour).UnixMilli()
	fixture.orders.nextID = "child-unknown"
	orders := keepPendingOrders{operatorOrderStub: fixture.orders, keep: map[string]bool{"child-unknown": true}}
	service := fixture.service()
	service.Orders = orders
	_, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'SUBMIT_UNKNOWN', c_submitted_at = 1 WHERE c_order_id = 'child-unknown'`).Error)
	orders.unknown = map[string]bool{"child-unknown": true}
	service.Orders = orders

	fixture.now = fixture.now.Add(2 * time.Hour)
	fixture.orders.specs = nil
	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status, "还有结果未知的子单，不能先记结束")
	require.Equal(t, "PARTIAL", result.FinishingStatus)
	require.Empty(t, fixture.orders.specs)

	// 解析出来退回待提交：恢复时丢弃并记 PARTIAL，不再下单。
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'PENDING' WHERE c_order_id = 'child-unknown'`).Error)
	orders.unknown = nil
	service.Orders = orders
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-unknown")
	require.NoError(t, err)
	require.NoError(t, service.ResumeOperatorAction(context.Background(), stored))
	stored, err = fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-unknown")
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", stored.Status)
	require.Equal(t, "CANCELED", childState(t, fixture, "child-unknown"))
	require.Empty(t, fixture.orders.specs)
	decoded, err := decodeFlattenResult(stored)
	require.NoError(t, err)
	require.Empty(t, decoded.FinishingStatus)
}

// 已解除禁止又过了截止时间：先看解除禁止，撤掉本动作的单后记 COMPLETED 并带“已解除禁止”标记，不记 PARTIAL。
func TestDelistUnblockBeatsDeadline(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-unblock", "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(time.Hour).UnixMilli()
	fixture.orders.nextID = "child-open"
	fixture.orders.leaveOpen = map[string]bool{"child-open": true}
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)

	_, err = fixture.store.UnblockInstrument(context.Background(), "BINANCE", "SWAP", "BTCUSDT")
	require.NoError(t, err)
	fixture.now = fixture.now.Add(2 * time.Hour)

	// 撤单还没确认：等撤单确认，不因为过了截止时间就压成 PARTIAL。
	result, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)

	fixture.orders.leaveOpen = nil
	result, err = fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", result.Action.Status)
	require.True(t, result.Unblocked)
	require.Equal(t, "CANCELED", childState(t, fixture, "child-open"))
}

// PARTIAL 后用同一个动作号重试且已过截止时间：保留上一轮各账户的进度（剩余仓位、原因），不清空。
func TestDelistDeadlineRetryKeepsProgress(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand("delist-progress", "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(time.Hour).UnixMilli()
	fixture.orders.nextID = ""
	_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'REJECTED', c_reject_reason = 'x', c_exchange_symbol = 'BTCUSDT' WHERE c_owner_id = 'delist-progress'`).Error)
	currentRoundOrders(t, fixture, "delist-progress")
	first, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", first.Action.Status)
	require.NotEmpty(t, first.Accounts)

	fixture.now = fixture.now.Add(2 * time.Hour)
	second, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", second.Action.Status)
	require.Equal(t, first.Accounts[0].TradingAccountID, second.Accounts[0].TradingAccountID)
	require.Len(t, second.Accounts[0].Remaining, len(first.Accounts[0].Remaining))
	require.Equal(t, first.Accounts[0].Remaining[0].Quantity, second.Accounts[0].Remaining[0].Quantity)
	require.Equal(t, first.Accounts[0].Remaining[0].Reason, second.Accounts[0].Remaining[0].Reason)
}

// 一轮执行中途过了截止时间：后面的单不再下。这里用现货、合约各有持仓的账户，第一笔（现货）提交时把时钟拨过截止时间，
// 同一轮的第二笔（合约）就不下了。
func TestDeadlineCheckedBeforeEveryOrder(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	command := flattenCommand("deadline-midway")
	command.DeadlineAt = fixture.now.Add(time.Minute).UnixMilli()
	fixture.orders.nextID = ""
	service := fixture.service()
	service.Orders = advanceOnSubmit{operatorOrderStub: fixture.orders, advance: func() { fixture.now = fixture.now.Add(2 * time.Minute) }}

	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1, "第一笔之后已过截止时间，第二笔不下")
	require.Equal(t, "PARTIAL", result.Action.Status)
	require.Contains(t, result.Accounts[0].Error, errDelistDeadlinePassed.Error())
}

// advanceOnSubmit 在每次提交时调用 advance（拨时钟）。
type advanceOnSubmit struct {
	*operatorOrderStub
	advance func()
}

func (o advanceOnSubmit) Submit(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	o.advance()
	return o.operatorOrderStub.Submit(ctx, spaceID, orderID)
}

// 整组清仓满 30 分钟记 PARTIAL 时同样丢弃本动作待提交的子单：恢复自动执行后不会被这笔单再暂停。
func TestFlattenTimeoutDiscardsOwnPendingChild(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.orders.nextID = ""
	const pending = "child-account-a-BTC-USDT-SPOT"
	orders := keepPendingOrders{operatorOrderStub: fixture.orders, keep: map[string]bool{pending: true}}
	service := fixture.service()
	service.Orders = orders
	command := flattenCommand("flatten-timeout")
	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)

	fixture.now = time.Now().Add(31 * time.Minute)
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "flatten-timeout")
	require.NoError(t, err)
	require.NoError(t, service.ResumeOperatorAction(context.Background(), stored))
	stored, err = fixture.store.GetOperatorAction(context.Background(), "space-1", "flatten-timeout")
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", stored.Status)
	require.Equal(t, "CANCELED", childState(t, fixture, pending))
}

// 下架清仓的 3% 限价夹进交易所的价格偏离限制：卖单下限 0.98 倍（用 90% 余量）时卖价不低于 98.2，免得被拒。
func TestDelistLimitPriceIsClampedToPriceBand(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	fixture.block(t, "SWAP", "BTCUSDT")
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_instruments SET c_min_sell_price_ratio = '0.98' WHERE c_instrument_id = 'BTC-USDT-SWAP'`).Error)
	fixture.store.InvalidateCaches()

	_, err := fixture.service().FlattenLogicalAccount(context.Background(), delistCommand("delist-band", "BTC-USDT-SWAP"))
	require.NoError(t, err)
	require.Len(t, fixture.orders.specs, 1)
	require.Equal(t, "98.2", fixture.orders.specs[0].LimitPrice.String())
}
