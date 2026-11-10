package target

import (
	"context"
	"errors"
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// expiryOrders 能解析结果未知的单（解析成挂单中）和确认撤单结果；err 非空时解析失败（模拟交易所查询失败）。
type expiryOrders struct {
	recoveringOrders
	err error
}

func (o expiryOrders) ResolveUnknown(ctx context.Context, spaceID, orderID string) (orderdomain.Order, error) {
	o.resolved = append(o.resolved, orderID)
	if o.err != nil {
		return orderdomain.Order{}, o.err
	}
	record, err := o.store.GetOrder(ctx, spaceID, orderID)
	if err != nil {
		return orderdomain.Order{}, err
	}
	expected := record.Version
	record.State, record.Version = string(orderdomain.Open), record.Version+1
	return orderdomain.Order{ID: shared.OrderID(orderID), State: orderdomain.Open}, o.store.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateOrder(record, expected)
	})
}

// 目标过期时有提交中、结果未知、撤单结果未知的 GTC 单：都解析或撤掉之后，目标才置为已过期。
func TestExpiryResolvesAndCancelsUnknownOrdersBeforeMarkingExpired(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.orders.store = f.store
	f.gtcOrderBy(t, "submitting", "target-current", "SUBMITTING", "instance-1")
	f.gtcOrderBy(t, "submit-unknown", "target-current", "SUBMIT_UNKNOWN", "instance-1")
	f.gtcOrderBy(t, "cancel-unknown", "target-current", "CANCEL_UNKNOWN", "instance-1")
	executor := f.executor()
	executor.Orders = expiryOrders{recoveringOrders: recoveringOrders{f.orders}}

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, result.Status)
	require.ElementsMatch(t, []string{"submitting", "submit-unknown"}, f.orders.resolved)
	require.ElementsMatch(t, []string{"submitting", "submit-unknown"}, f.orders.canceled)
	for _, id := range []string{"submitting", "submit-unknown"} {
		order, err := f.store.GetOrder(context.Background(), "space-1", id)
		require.NoError(t, err)
		require.Equal(t, "OPEN", order.State, id)
	}
	order, err := f.store.GetOrder(context.Background(), "space-1", "cancel-unknown")
	require.NoError(t, err)
	require.Equal(t, "CANCELED", order.State, "撤单结果未知的单走了恢复")
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, StatusExpired, current.Status)
}

// 交易所查询失败、结果解析不出来时：目标保持原状，按 expiryRetryDelay 重试。
func TestExpiryRetriesWhenUnknownOrderCannotBeResolved(t *testing.T) {
	f := newTargetFixture(t, exchange.MarketTypeSwap)
	modernExpiryTarget(t, f)
	f.orders.store = f.store
	f.gtcOrderBy(t, "submit-unknown", "target-current", "SUBMIT_UNKNOWN", "instance-1")
	queryFailed := errors.New("交易所查询失败")
	executor := f.executor()
	executor.Orders = expiryOrders{recoveringOrders: recoveringOrders{f.orders}, err: queryFailed}

	result, err := executor.Converge(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, queryFailed)
	require.Equal(t, expiryRetryDelay, result.RetryAfter)
	current, err := f.store.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.NotEqual(t, StatusExpired, current.Status)
	require.Empty(t, f.orders.canceled)
}
