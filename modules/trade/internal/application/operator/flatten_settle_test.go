package operator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
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

// failResolveOrders 让指定子单的解析一直报 resolveErr（模拟查单一直失败）。
type failResolveOrders struct {
	keepPendingOrders
	resolveErr map[string]error
}

func (o failResolveOrders) ResolveUnknown(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	if err := o.resolveErr[orderID]; err != nil {
		return orderdomain.Order{}, err
	}
	return o.keepPendingOrders.ResolveUnknown(ctx, spaceID, orderID)
}

// startFinishingWithUnknownChild 让下架清仓留下一笔结果未知的子单并过截止时间，返回带着这笔子单解析错误的服务。
func startFinishingWithUnknownChild(t *testing.T, fixture *operatorFixture, actionID, childID string, resolveErr error) (*Service, FlattenCommand) {
	t.Helper()
	fixture.block(t, "SWAP", "BTCUSDT")
	command := delistCommand(actionID, "BTC-USDT-SWAP")
	command.DeadlineAt = fixture.now.Add(time.Hour).UnixMilli()
	fixture.orders.nextID = childID
	keep := keepPendingOrders{operatorOrderStub: fixture.orders, keep: map[string]bool{childID: true}}
	service := fixture.service()
	service.Orders = keep
	_, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'SUBMIT_UNKNOWN', c_submitted_at = 1 WHERE c_order_id = ?`, childID).Error)
	service.Orders = failResolveOrders{keepPendingOrders: keep, resolveErr: map[string]error{childID: resolveErr}}
	fixture.now = fixture.now.Add(2 * time.Hour)
	fixture.orders.specs = nil
	return service, command
}

func resumeStored(t *testing.T, fixture *operatorFixture, service *Service, actionID string) FlattenResult {
	t.Helper()
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", actionID)
	require.NoError(t, err)
	require.NoError(t, service.ResumeOperatorAction(context.Background(), stored), "子单解析失败是本动作的账户问题，不能让工作线程报错")
	stored, err = fixture.store.GetOperatorAction(context.Background(), "space-1", actionID)
	require.NoError(t, err)
	decoded, err := decodeFlattenResult(stored)
	require.NoError(t, err)
	return decoded
}

// 收尾时子单解析一直报错（例如凭证失效）：动作保持运行最多 30 分钟（从进入收尾算起，重启后照样算），
// 到点后照常落成记下的 PARTIAL，没收拾掉的子单写进结果，要人工核对；期间不再下单。
func TestFlattenFinishingGivesUpAfterTimeout(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	authErr := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	service, command := startFinishingWithUnknownChild(t, fixture, "delist-stuck", "child-stuck", authErr)
	enteredAt := fixture.now

	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err, "解析失败不作为调用错误返回")
	require.Equal(t, "RUNNING", result.Action.Status)
	require.Equal(t, "PARTIAL", result.FinishingStatus)
	require.Equal(t, enteredAt.UnixMilli(), result.FinishingSince)
	require.Contains(t, result.Action.LastError, "child-stuck")

	// 29 分钟后（换一个服务实例，模拟重启）：进入收尾的时间从库里读出，还没到上限。
	fixture.now = enteredAt.Add(29 * time.Minute)
	restarted := fixture.service()
	restarted.Orders = service.Orders
	decoded := resumeStored(t, fixture, restarted, "delist-stuck")
	require.Equal(t, "RUNNING", decoded.Action.Status)
	require.Equal(t, enteredAt.UnixMilli(), decoded.FinishingSince, "重启后不重新计时")

	// 满 30 分钟：落成 PARTIAL，子单号写进结果。
	fixture.now = enteredAt.Add(30 * time.Minute)
	decoded = resumeStored(t, fixture, restarted, "delist-stuck")
	require.Equal(t, "PARTIAL", decoded.Action.Status)
	require.Equal(t, []string{"child-stuck"}, decoded.ManualCheckOrderIDs)
	require.Empty(t, decoded.FinishingStatus)
	require.Zero(t, decoded.FinishingSince)
	require.Equal(t, "SUBMIT_UNKNOWN", childState(t, fixture, "child-stuck"), "收拾不了的子单原样留着，等人工核对")
	require.Empty(t, fixture.orders.specs, "收尾期间不下单")
	require.NotEmpty(t, telemetry.Recent(telemetry.AttentionFlattenOrdersUnsettled, fixture.now, time.Minute))
}

// 收尾时解析子单一直遇到写库失败：收拾子单尽力而为，错误只记进动作的 last_error、不作为调用错误返回（不影响 Trade 就绪），
// 30 分钟内保持运行；满 30 分钟不再无限运行，没收拾掉的子单并入人工核对，照常落成记下的 PARTIAL 并记关注项。
func TestFlattenFinishingGivesUpOnSharedErrorAfterTimeout(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	service, command := startFinishingWithUnknownChild(t, fixture, "delist-shared", "child-shared", errors.New("disk I/O error"))
	enteredAt := fixture.now

	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err, "收拾子单的错误只记在动作上")
	require.Equal(t, "RUNNING", result.Action.Status)
	require.Equal(t, "PARTIAL", result.FinishingStatus)
	require.Equal(t, enteredAt.UnixMilli(), result.FinishingSince)

	resume := func() (FlattenResult, error) {
		stored, getErr := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-shared")
		require.NoError(t, getErr)
		resumeErr := service.ResumeOperatorAction(context.Background(), stored)
		stored, getErr = fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-shared")
		require.NoError(t, getErr)
		decoded, decodeErr := decodeFlattenResult(stored)
		require.NoError(t, decodeErr)
		return decoded, resumeErr
	}
	fixture.now = enteredAt.Add(29 * time.Minute)
	decoded, err := resume()
	require.NoError(t, err)
	require.Equal(t, "RUNNING", decoded.Action.Status)
	require.Contains(t, decoded.Action.LastError, "disk I/O error")
	require.Equal(t, enteredAt.UnixMilli(), decoded.FinishingSince, "不重新计时")

	fixture.now = enteredAt.Add(30 * time.Minute)
	decoded, err = resume()
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", decoded.Action.Status)
	require.Equal(t, []string{"child-shared"}, decoded.ManualCheckOrderIDs)
	require.Empty(t, decoded.FinishingStatus)
	require.Zero(t, decoded.FinishingSince)
	require.Contains(t, decoded.Action.LastError, "disk I/O error")
	require.Equal(t, "SUBMIT_UNKNOWN", childState(t, fixture, "child-shared"), "收拾不了的子单原样留着，等人工核对")
	require.Empty(t, fixture.orders.specs, "收尾期间不下单")
	found := false
	for _, detail := range telemetry.Recent(telemetry.AttentionFlattenOrdersUnsettled, fixture.now, time.Minute) {
		found = found || strings.Contains(detail, "delist-shared") && strings.Contains(detail, "没收拾掉")
	}
	require.True(t, found, "要记关注项")
}

// operatorChild 在 accountID 上直接登记本动作的一笔子单（这个账户已开通市场上的限价卖单），用来摆出别的成员上还没结束的子单。
func operatorChild(t *testing.T, fixture *operatorFixture, accountID, actionID, orderID, state string) {
	t.Helper()
	limit := "97"
	instrument := fixture.instrument(accountID)
	fixture.order(t, store.OrderRecord{
		SpaceID: "space-1", TradingAccountID: accountID, OrderID: orderID, ClientOrderID: orderID,
		MarketType: instrument.MarketType, InstrumentID: instrument.InstrumentID, ExchangeSymbol: instrument.ExchangeSymbol,
		OrderType: "LIMIT", TimeInForce: "IOC", Side: "SELL", Quantity: "1", LimitPrice: &limit, ReferencePrice: "100",
		ReservedAsset: "BTC", ReservedQuantity: "1", RemainingReservedQuantity: "1", SubmittedAt: 1,
		State: state, OwnerType: "OPERATOR", OwnerID: actionID, LogicalAccountID: "logical-1", Version: 1,
	})
}

// unsettledNoted 报告关注项 flatten_orders_unsettled 里有没有提到 actionID 且含 needle 的详情。
func unsettledNoted(fixture *operatorFixture, actionID, needle string) bool {
	for _, detail := range telemetry.Recent(telemetry.AttentionFlattenOrdersUnsettled, fixture.now, time.Minute) {
		if strings.Contains(detail, actionID) && strings.Contains(detail, needle) {
			return true
		}
	}
	return false
}

// 收尾时这次调用的预算用完（解析成员 A 的结果未知子单时上下文被取消）：所有成员的子单在第一步只读写本地库时已经列全，
// 成员 B 的待提交子单照样被丢弃（释放资金预留）；A 那笔留到下次再解析，不记“读写不了子单”的关注项。
// 预算用完不能让收尾无限拖下去：满 30 分钟时照常落成，A 那笔交给人工核对。
func TestFlattenFinishingBudgetEndDiscardsPendingAndRespectsTimeout(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	authErr := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	service, command := startFinishingWithUnknownChild(t, fixture, "delist-budget", "child-budget-a", authErr)
	enteredAt := fixture.now
	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	require.Equal(t, enteredAt.UnixMilli(), result.FinishingSince)
	operatorChild(t, fixture, "account-b", "delist-budget", "child-budget-b", "PENDING")
	failing := service.Orders.(failResolveOrders)

	resumeCanceled := func() FlattenResult {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service.Orders = cancelOnResolve{failResolveOrders: failing, cancel: cancel}
		stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-budget")
		require.NoError(t, err)
		err = service.ResumeOperatorAction(ctx, stored)
		require.True(t, err == nil || ActionScopedError(err), "预算用完不能被当成共享故障：%v", err)
		stored, err = fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-budget")
		require.NoError(t, err)
		decoded, err := decodeFlattenResult(stored)
		require.NoError(t, err)
		return decoded
	}

	// 上限之内：保持收尾，B 的待提交子单已丢弃，A 那笔留给下次。
	fixture.now = enteredAt.Add(10 * time.Minute)
	decoded := resumeCanceled()
	require.Equal(t, "RUNNING", decoded.Action.Status)
	require.Equal(t, enteredAt.UnixMilli(), decoded.FinishingSince, "不重新计时")
	require.Equal(t, "CANCELED", childState(t, fixture, "child-budget-b"), "第一步只读写本地库，预算用完也照样丢弃待提交子单")
	require.Equal(t, "SUBMIT_UNKNOWN", childState(t, fixture, "child-budget-a"))
	require.False(t, unsettledNoted(fixture, "delist-budget", "读写不了子单"), "不能记成共享故障")

	// 满 30 分钟预算仍每次用完：照常落成，A 交给人工核对。
	fixture.now = enteredAt.Add(30 * time.Minute)
	decoded = resumeCanceled()
	require.Equal(t, "PARTIAL", decoded.Action.Status)
	require.Equal(t, []string{"child-budget-a"}, decoded.ManualCheckOrderIDs)
	require.True(t, unsettledNoted(fixture, "delist-budget", "需人工核对"))
	require.False(t, unsettledNoted(fixture, "delist-budget", "读写不了子单"), "不能记成共享故障")
	require.Empty(t, fixture.orders.specs, "收尾期间不下单")
}

// 收尾满 30 分钟时成员 A 的子单解析仍写库失败：B 的待提交子单在解析之前已丢弃（释放资金预留），
// A 那笔交给人工核对，照常落成并记关注项；A 的失败不挡 B 的结果未知子单（B 的照样去解析）。
func TestFlattenFinishingSharedErrorListsChildrenOfAllMembers(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	service, command := startFinishingWithUnknownChild(t, fixture, "delist-shared-all", "child-shared-a", errors.New("disk I/O error"))
	enteredAt := fixture.now
	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status)
	operatorChild(t, fixture, "account-b", "delist-shared-all", "child-shared-b-pending", "PENDING")
	operatorChild(t, fixture, "account-b", "delist-shared-all", "child-shared-b-unknown", "SUBMIT_UNKNOWN")

	fixture.now = enteredAt.Add(30 * time.Minute)
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-shared-all")
	require.NoError(t, err)
	require.NoError(t, service.ResumeOperatorAction(context.Background(), stored))
	stored, err = fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-shared-all")
	require.NoError(t, err)
	decoded, err := decodeFlattenResult(stored)
	require.NoError(t, err)
	require.Equal(t, "PARTIAL", decoded.Action.Status)
	require.Equal(t, []string{"child-shared-a"}, decoded.ManualCheckOrderIDs, "只有 A 那笔没收拾掉")
	require.Equal(t, "CANCELED", childState(t, fixture, "child-shared-b-pending"), "待提交的子单丢弃，释放资金预留")
	require.NotEqual(t, "SUBMIT_UNKNOWN", childState(t, fixture, "child-shared-b-unknown"), "A 的失败不挡 B，B 的结果未知子单照样解析")
	require.True(t, unsettledNoted(fixture, "delist-shared-all", "没收拾掉"))
	require.Empty(t, fixture.orders.specs, "收尾期间不下单")
}

// 交易所确认标的不存在（币安 -1121、OKX 51001）：这笔子单等多久都收拾不了，不阻塞收尾，
// 动作当场落成 PARTIAL，子单记为需人工核对。
func TestFlattenFinishingReleasesUnsettleableChild(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"币安标的不存在", &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
			Err: &exchange.Error{Kind: exchange.ErrorRejected, Code: exchange.CodeBinanceInvalidSymbol}}},
		{"OKX 标的不存在", &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
			Err: &exchange.Error{Kind: exchange.ErrorRejected, Code: exchange.CodeOKXInstrumentNotFound}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMixedFlattenFixture(t)
			service, command := startFinishingWithUnknownChild(t, fixture, "delist-gone", "child-gone", tc.err)

			result, err := service.FlattenLogicalAccount(context.Background(), command)
			require.NoError(t, err)
			require.Equal(t, "PARTIAL", result.Action.Status)
			require.Equal(t, []string{"child-gone"}, result.ManualCheckOrderIDs)
			require.Empty(t, result.FinishingStatus)
			require.Empty(t, fixture.orders.specs)
		})
	}
}

// 取不到账户会话（重启后会话还没建好）只是暂时的：不当场放行，和凭证失效一样保持收尾，
// 满 30 分钟仍取不到才落成 PARTIAL、子单记为需人工核对。
func TestFlattenFinishingWaitsForMissingSession(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	noSession := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "adapter",
		Err: errors.New("交易账户会话还没有启动，请稍后重试")}
	service, command := startFinishingWithUnknownChild(t, fixture, "delist-nosession", "child-nosession", noSession)
	enteredAt := fixture.now

	result, err := service.FlattenLogicalAccount(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "RUNNING", result.Action.Status, "没有会话不当场放行")
	require.Equal(t, "PARTIAL", result.FinishingStatus)
	require.Empty(t, result.ManualCheckOrderIDs)

	fixture.now = enteredAt.Add(29 * time.Minute)
	decoded := resumeStored(t, fixture, service, "delist-nosession")
	require.Equal(t, "RUNNING", decoded.Action.Status)

	fixture.now = enteredAt.Add(30 * time.Minute)
	decoded = resumeStored(t, fixture, service, "delist-nosession")
	require.Equal(t, "PARTIAL", decoded.Action.Status)
	require.Equal(t, []string{"child-nosession"}, decoded.ManualCheckOrderIDs)
	require.Empty(t, decoded.FinishingStatus)
	require.Empty(t, fixture.orders.specs, "收尾期间不下单")
}

// 工作线程按 ActionScopedError 隔离单个动作的失败：账户、交易所侧的错误只记在这个动作上；掺着共享故障的不算。
func TestActionScopedErrorClassifiesAccountErrors(t *testing.T) {
	account := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication}}
	require.True(t, ActionScopedError(fmt.Errorf("解析子单 c1：%w", account)))
	require.True(t, ActionScopedError(errors.Join(account, ErrCancelUnconfirmed)))
	require.False(t, ActionScopedError(errors.Join(account, errors.New("database is locked"))))
	require.False(t, ActionScopedError(nil))
}

// cancelOnResolve 在解析子单时让调用上下文结束（模拟本次调用的预算刚好到点），并原样带回上下文的错误。
type cancelOnResolve struct {
	failResolveOrders
	cancel context.CancelFunc
}

func (o cancelOnResolve) ResolveUnknown(ctx context.Context, _ string, _ string) (orderdomain.Order, error) {
	o.cancel()
	return orderdomain.Order{}, ctx.Err()
}

// 收拾子单尽力而为：解析、丢弃遇到的任何错误（写库失败、凭证失效、版本冲突、预算用完）都只写进动作的 last_error，
// 不作为调用错误返回（不影响 Trade 的整体就绪），动作保持收尾，下次恢复再收拾。
func TestSettleFlattenChildrenKeepsErrorsOnTheAction(t *testing.T) {
	authErr := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	for _, tc := range []struct {
		name    string
		err     error
		pending bool
		budget  bool
	}{
		{name: "解析时写库失败", err: errors.New("disk I/O error")},
		{name: "丢弃时写库失败", err: errors.New("disk I/O error"), pending: true},
		{name: "解析时凭证失效", err: authErr},
		{name: "丢弃时撤单冲突", err: fmt.Errorf("discard: %w", store.ErrConflict), pending: true},
		{name: "解析时预算到点", budget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMixedFlattenFixture(t)
			resolveErr := tc.err
			if tc.pending {
				resolveErr = nil
			}
			service, command := startFinishingWithUnknownChild(t, fixture, "delist-settle", "child-settle", resolveErr)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.pending {
				require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_state = 'PENDING' WHERE c_order_id = 'child-settle'`).Error)
				fixture.orders.cancelError = map[string]error{"child-settle": tc.err}
			}
			if tc.budget {
				service.Orders = cancelOnResolve{failResolveOrders: service.Orders.(failResolveOrders), cancel: cancel}
			}

			result, err := service.FlattenLogicalAccount(ctx, command)
			require.Equal(t, "RUNNING", result.Action.Status)
			require.Equal(t, "PARTIAL", result.FinishingStatus, "收拾不完就保持收尾")
			require.NoError(t, err, "收拾子单的错误只记在动作上，不作为调用错误返回")
			stored, getErr := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-settle")
			require.NoError(t, getErr)
			require.Contains(t, stored.LastError, "child-settle")
		})
	}
}

// 成员在发起之后被移出组合、订单版本冲突（私有流并发改了同一行）只关乎这个动作，不让操作工作线程不就绪。
func TestActionScopedErrorCoversMemberRemovalAndConflict(t *testing.T) {
	require.True(t, ActionScopedError(errors.Join(ErrNotInLogicalAccount, nil)))
	require.True(t, ActionScopedError(fmt.Errorf("cancel order o1: %w", fmt.Errorf("%w: stale order version", store.ErrConflict))))
	require.False(t, ActionScopedError(errors.Join(ErrNotInLogicalAccount, errors.New("database is locked"))))
	require.False(t, submissionBusinessError(store.ErrConflict), "版本冲突仍要重试，不算业务错误")

	// 指定的执行账户、交易所在发起之后被移出组合（动作已创建，走成员筛选那两处）：动作按失败结束，返回的错误只关乎这个动作；
	// 工作线程恢复运行中的动作拿到的也是这个错误形状。
	for _, tc := range []struct{ name, account, exchange, instrument string }{
		{"执行账户被移出", "account-gone", "", ""},
		{"交易所没有成员", "", "BYBIT", "BTC-USDT-SWAP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMixedFlattenFixture(t)
			command := flattenCommand("flatten-removed")
			command.TradingAccountID, command.Exchange, command.InstrumentID = tc.account, tc.exchange, tc.instrument
			_, err := fixture.service().FlattenLogicalAccount(context.Background(), command)
			require.ErrorIs(t, err, ErrNotInLogicalAccount)
			require.True(t, ActionScopedError(err))
			stored, getErr := fixture.store.GetOperatorAction(context.Background(), "space-1", "flatten-removed")
			require.NoError(t, getErr)
			require.Equal(t, "FAILED", stored.Status)

			require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_operator_actions SET c_status = 'RUNNING' WHERE c_action_id = 'flatten-removed'`).Error)
			stored, getErr = fixture.store.GetOperatorAction(context.Background(), "space-1", "flatten-removed")
			require.NoError(t, getErr)
			err = fixture.service().ResumeOperatorAction(context.Background(), stored)
			require.Error(t, err)
			require.True(t, ActionScopedError(err), "工作线程恢复时拿到的错误不能拖垮就绪：%v", err)
		})
	}
}

// blockDiscard 让 account 上子单的丢弃一直等到上下文结束（模拟交易账户锁被一个卡在交易所调用上的同步占着），
// 和真实的 DiscardPending 一样带回 Operation 为等锁的 AccountExecutionError；attempts 记下每笔丢弃尝试。
type blockDiscard struct {
	failResolveOrders
	account  string
	attempts *[]string
}

func (o blockDiscard) DiscardPending(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	*o.attempts = append(*o.attempts, orderID)
	record, err := o.fixture.store.GetOrder(context.Background(), spaceID, orderID)
	if err == nil && record.TradingAccountID == o.account {
		<-ctx.Done()
		return orderdomain.Order{}, &orderapp.AccountExecutionError{TradingAccountID: o.account, Operation: orderapp.OperationDiscardPendingLock, Err: ctx.Err()}
	}
	return o.failResolveOrders.DiscardPending(ctx, spaceID, orderID)
}

// 收尾时成员 A 的待提交子单丢弃要等账户锁、等到时限到期：只记在本动作上，不作为调用错误返回（不让 Trade 不就绪）；
// A 余下的待提交子单这一轮不再试（锁被占着，再等也是白等），成员 B 的待提交子单当轮照样丢弃。
func TestFlattenFinishingDiscardLockWaitIsActionScoped(t *testing.T) {
	previous := settleLocalTimeout
	settleLocalTimeout = 50 * time.Millisecond
	t.Cleanup(func() { settleLocalTimeout = previous })
	fixture := newMixedFlattenFixture(t)
	authErr := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	service, _ := startFinishingWithUnknownChild(t, fixture, "delist-lock", "child-lock-unknown", authErr)
	operatorChild(t, fixture, "account-a", "delist-lock", "child-lock-a", "PENDING")
	operatorChild(t, fixture, "account-a", "delist-lock", "child-lock-a2", "PENDING")
	operatorChild(t, fixture, "account-b", "delist-lock", "child-lock-b", "PENDING")
	var attempts []string
	service.Orders = blockDiscard{failResolveOrders: service.Orders.(failResolveOrders), account: "account-a", attempts: &attempts}

	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-lock")
	require.NoError(t, err)
	err = service.ResumeOperatorAction(context.Background(), stored)
	require.NoError(t, err, "等账户锁超时只记在本动作上")
	require.Equal(t, "PENDING", childState(t, fixture, "child-lock-a"))
	require.Equal(t, "PENDING", childState(t, fixture, "child-lock-a2"))
	require.Equal(t, "CANCELED", childState(t, fixture, "child-lock-b"), "B 的待提交子单当轮丢弃")
	require.Equal(t, []string{"child-lock-a", "child-lock-b"}, attempts, "A 等锁超时一次后，它余下的待提交子单这一轮不再试")
	decoded := resumeStoredNoAssert(t, fixture, "delist-lock")
	require.Equal(t, "RUNNING", decoded.Action.Status)
	require.False(t, unsettledNoted(fixture, "delist-lock", "读写不了子单"))
}

// hangResolve 让指定子单的解析一直挂到上下文结束（模拟交易所查单不响应）；other 里的子单解析后退回待提交。
type hangResolve struct {
	failResolveOrders
	hang  string
	other string
}

func (o hangResolve) ResolveUnknown(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	switch orderID {
	case o.hang:
		<-ctx.Done()
		return orderdomain.Order{}, ctx.Err()
	case o.other:
		return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Pending}, nil
	}
	return o.failResolveOrders.ResolveUnknown(ctx, spaceID, orderID)
}

// 成员 A 的结果未知子单解析一直挂住：剩余预算平均分给每笔，A 那笔到自己的时限就放下，成员 B 的子单照样轮得到；
// B 那笔解析后退回待提交，当轮丢弃（释放资金预留）。
func TestFlattenFinishingHungResolveDoesNotStarveOtherMembers(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	authErr := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	service, _ := startFinishingWithUnknownChild(t, fixture, "delist-hang", "child-hang-first", authErr)
	operatorChild(t, fixture, "account-a", "delist-hang", "child-hang-a", "SUBMIT_UNKNOWN")
	operatorChild(t, fixture, "account-b", "delist-hang", "child-ok-b", "SUBMIT_UNKNOWN")
	service.Orders = hangResolve{failResolveOrders: service.Orders.(failResolveOrders), hang: "child-hang-a", other: "child-ok-b"}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", "delist-hang")
	require.NoError(t, err)
	err = service.ResumeOperatorAction(ctx, stored)
	require.NoError(t, err, "解析挂住只记在本动作上")
	require.Equal(t, "SUBMIT_UNKNOWN", childState(t, fixture, "child-hang-a"))
	require.Equal(t, "CANCELED", childState(t, fixture, "child-ok-b"), "B 的子单轮得到，解析后退回待提交就丢弃")
	require.Equal(t, "RUNNING", resumeStoredNoAssert(t, fixture, "delist-hang").Action.Status)
}

// resumeStoredNoAssert 只读出动作当前的结果，不再恢复一次。
func resumeStoredNoAssert(t *testing.T, fixture *operatorFixture, actionID string) FlattenResult {
	t.Helper()
	stored, err := fixture.store.GetOperatorAction(context.Background(), "space-1", actionID)
	require.NoError(t, err)
	decoded, err := decodeFlattenResult(stored)
	require.NoError(t, err)
	return decoded
}

// 某个账户的子单列不出来（读库报错，这里用一行坏数据模拟）：记为没列全，其余账户照常收拾；未满 30 分钟保持收尾，
// 不作为调用错误返回；满 30 分钟另列一次后照常落成，关注项写明没能列全。
func TestFlattenFinishingIncompleteListingRespectsTimeout(t *testing.T) {
	fixture := newMixedFlattenFixture(t)
	authErr := &orderapp.AccountExecutionError{TradingAccountID: "account-a", Operation: "get_order",
		Err: &exchange.Error{Kind: exchange.ErrorAuthentication, Code: "-2015"}}
	service, _ := startFinishingWithUnknownChild(t, fixture, "delist-list", "child-list-unknown", authErr)
	enteredAt := fixture.now
	operatorChild(t, fixture, "account-a", "delist-list", "child-list-a", "PENDING")
	operatorChild(t, fixture, "account-b", "delist-list", "child-list-bad", "PENDING")
	require.NoError(t, fixture.store.DBForTest().Exec(`UPDATE t_trade_orders SET c_version = 'bad' WHERE c_order_id = 'child-list-bad'`).Error)
	_, listErr := fixture.store.ListActiveOrdersForAccount(context.Background(), "space-1", "account-b")
	require.Error(t, listErr, "坏数据要让列子单报错")

	decoded := resumeStored(t, fixture, service, "delist-list")
	require.Equal(t, "RUNNING", decoded.Action.Status, "没列全就保持收尾")
	require.Equal(t, enteredAt.UnixMilli(), decoded.FinishingSince)
	fixture.now = enteredAt.Add(29 * time.Minute)
	decoded = resumeStored(t, fixture, service, "delist-list")
	require.Equal(t, "RUNNING", decoded.Action.Status)
	require.Equal(t, enteredAt.UnixMilli(), decoded.FinishingSince, "不重新计时")
	require.Equal(t, "CANCELED", childState(t, fixture, "child-list-a"), "其余账户照常收拾")
	require.Contains(t, decoded.Action.LastError, "account-b")

	fixture.now = enteredAt.Add(30 * time.Minute)
	decoded = resumeStored(t, fixture, service, "delist-list")
	require.Equal(t, "PARTIAL", decoded.Action.Status)
	require.True(t, unsettledNoted(fixture, "delist-list", "没能列全"))
	require.Empty(t, fixture.orders.specs, "收尾期间不下单")
}
