package order

import (
	"context"
	"testing"
	"time"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

// 订单号重复被适配器归为结果未知：单笔下单进入提交结果未知，下一次恢复查到原单并认领。
func TestDuplicateClientOrderIDSingleSubmitIsUnknownThenClaimed(t *testing.T) {
	service, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Code: "-2010"}
	pending, err := service.Place(context.Background(), "space-1", testSpec(service.now()))
	require.NoError(t, err)
	unknown, err := service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)
	require.Equal(t, orderdomain.SubmitUnknown, unknown.State)

	adapter.getErr = nil
	adapter.getResult = exchange.Order{Market: exchange.MarketTypeSwap, ExchangeOrderID: "original-order"}
	resolved, err := service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, resolved.State)
	require.Equal(t, "original-order", resolved.ExchangeOrderID)
}

// 批量下单同样：订单号重复的那一笔进入提交结果未知，批量恢复查到原单并认领，其余照常。
func TestDuplicateClientOrderIDBatchSubmitIsUnknownThenClaimed(t *testing.T) {
	service, adapter, ids := batchService(t, 2)
	adapter.errs["client-1"] = &exchange.Error{Kind: exchange.ErrorTransportUnknown, Code: "-4116"}
	results, err := service.SubmitBatch(context.Background(), "space-1", ids)
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, results[0].Order.State)
	require.Equal(t, orderdomain.SubmitUnknown, results[1].Order.State)

	adapter.adapterStub.getResult = exchange.Order{Market: exchange.MarketTypeSwap, ExchangeOrderID: "original-order"}
	resolved, err := service.ResolveUnknownBatch(context.Background(), "space-1", ids[1:])
	require.NoError(t, err)
	require.Equal(t, orderdomain.Open, resolved[0].Order.State)
	require.Equal(t, "original-order", resolved[0].Order.ExchangeOrderID)
}

// 查询返回不带错误码的 404（网关错误页）不算交易所确认没有这张单：超过窗口也不退回待提交。
func TestUnknownRecoveryBareNotFoundKeepsUnknown(t *testing.T) {
	service, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	base := time.Unix(1_700_000_000, 0)
	pending, err := service.Place(context.Background(), "space-1", testSpec(base))
	require.NoError(t, err)
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)

	adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound, HTTPStatus: 404}
	service.Now = func() time.Time { return base.Add(10 * time.Minute) }
	_, getErr := service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.Error(t, getErr)
	stored, err := service.Store.GetOrder(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.SubmitUnknown, orderdomain.State(stored.State))
}

// 默认观察窗口是 2 分钟：-2013 确认不存在后，2 分钟内保持结果未知，满 2 分钟才退回待提交。
func TestUnknownRecoveryDefaultWindowIsTwoMinutes(t *testing.T) {
	service, _, adapter := newTestService(t)
	adapter.placeErr = &exchange.Error{Kind: exchange.ErrorTransportUnknown}
	base := time.Unix(1_700_000_000, 0)
	pending, err := service.Place(context.Background(), "space-1", testSpec(base))
	require.NoError(t, err)
	_, err = service.Submit(context.Background(), "space-1", string(pending.ID))
	require.Error(t, err)

	adapter.getErr = &exchange.Error{Kind: exchange.ErrorOrderNotFound, Code: "-2013"}
	service.Now = func() time.Time { return base.Add(61 * time.Second) }
	resolved, err := service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.SubmitUnknown, resolved.State)
	service.Now = func() time.Time { return base.Add(121 * time.Second) }
	resolved, err = service.ResolveUnknown(context.Background(), "space-1", string(pending.ID))
	require.NoError(t, err)
	require.Equal(t, orderdomain.Pending, resolved.State)
}
