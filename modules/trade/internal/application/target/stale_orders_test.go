package target

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// gtcOrder 建一笔限价 GTC 的目标单（account-a，合约），归属 ownerID 这份目标。
func (f *targetFixture) gtcOrder(t *testing.T, orderID, ownerID, state string) {
	t.Helper()
	f.gtcOrderBy(t, orderID, ownerID, state, "runner-1")
}

// gtcOrderBy 同 gtcOrder，可指定提交这笔单的策略实例。
func (f *targetFixture) gtcOrderBy(t *testing.T, orderID, ownerID, state, runnerID string) {
	t.Helper()
	f.order(t, store.OrderRecord{
		SpaceID: "space-1", OrderID: orderID, TradingAccountID: "account-a", LogicalAccountID: "logical-1", RunnerID: runnerID,
		ClientOrderID: orderID, ExchangeOrderID: "exchange-" + orderID, Exchange: "BINANCE", MarketType: "SWAP",
		InstrumentID: "BTC-USDT-SWAP", ExchangeSymbol: "BTCUSDT",
		OrderType: "LIMIT", TimeInForce: "GTC", Side: "BUY", Quantity: "1", LimitPrice: strPtr("99.9"), ReferencePrice: "100",
		OwnerType: "TARGET", OwnerID: ownerID, State: state, SubmittedAt: f.now.UnixMilli(), Version: 1,
	})
}

// 上一份目标留下 5 笔挂单：新目标第 1 阶段一轮内全部发出撤单，当前目标自己的挂单不动。
func TestStaleTargetOrdersAreAllCanceledInOneRound(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	var want []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("stale-%d", i)
		f.gtcOrder(t, id, "target-old", "OPEN")
		want = append(want, id)
	}
	f.gtcOrder(t, "own", "target-current", "OPEN")

	result, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusConverging, result.Status)
	require.Equal(t, "stop", result.Action)
	require.Positive(t, result.RetryAfter)
	require.ElementsMatch(t, want, f.orders.canceled, "旧挂单一轮内全部撤，自己的挂单不撤")
}

// 排头是撤单结果未知的旧单、确认不了时：后面的旧挂单照常撤，错误留给下一轮重试；确认得了就不再报错。
func TestStaleCancelUnknownDoesNotBlockTheRest(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.gtcOrder(t, "a-cancel-unknown", "target-old", "CANCEL_UNKNOWN")
	for i := 0; i < 3; i++ {
		f.gtcOrder(t, fmt.Sprintf("stale-%d", i), "target-old", "OPEN")
	}
	executor := f.restingExecutor()

	_, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, errTargetOrderUnresolved, "撤单结果确认不了，报错重试")
	require.ElementsMatch(t, []string{"stale-0", "stale-1", "stale-2"}, f.orders.canceled, "后面的旧挂单照常撤")

	f.orders.store = f.store
	executor.Orders = recoveringOrders{f.orders}
	_, err = executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	order, err := f.store.GetOrder(context.Background(), "space-1", "a-cancel-unknown")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", order.State, "走了撤单结果恢复")
}

// 旧目标留下待提交、结果未知的单：待提交的丢弃，结果未知的先解析。
func TestStalePendingAndUnknownOrdersAreDiscardedOrResolved(t *testing.T) {
	f := swapPolicyFixture(t, restingSettings)
	f.now = time.Now().UTC()
	f.orders.store = f.store
	f.target(t, []store.InstrumentTarget{{InstrumentID: "BTC-USDT-SWAP", Quantity: "10"}})
	f.gtcOrder(t, "stale-pending", "target-old", "PENDING")
	f.gtcOrder(t, "stale-unknown", "target-old", "SUBMIT_UNKNOWN")

	_, err := f.restingExecutor().Converge(context.Background(), "space-1", "logical-1")
	// 桩的解析不改订单状态：限价 GTC 单解析不出来要报错重试。
	require.ErrorIs(t, err, errTargetOrderUnresolved)
	require.Equal(t, []string{"stale-pending"}, f.orders.discarded)
	require.Equal(t, []string{"stale-unknown"}, f.orders.resolved)
}
