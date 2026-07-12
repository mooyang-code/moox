package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kernelTradeAdapter struct{}

func (kernelTradeAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-kernel", Status: "OPEN"}, nil
}
func (kernelTradeAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (kernelTradeAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-kernel", Status: "OPEN"}, nil
}
func (kernelTradeAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (kernelTradeAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (kernelTradeAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func newKernelTradeServer(t *testing.T) (*Server, *command.Engine) {
	t.Helper()
	ks := openKernelStore(t)
	seedKernelBalance(t, ks)
	engine := &command.Engine{Store: ks, Adapter: kernelTradeAdapter{}}
	return New(nil, engine), engine
}

func seedKernelOpenOrder(t *testing.T, engine *command.Engine, orderID string) {
	t.Helper()
	ctx := rpcCtx(t, "crypto", "user-1")
	placed, err := engine.Place(ctx, command.PlaceInput{
		SpaceID: "crypto", OrderID: orderID, ClientOrderID: orderID + "-cli",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	_, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "crypto", placed.OrderID)
	require.NoError(t, err)
}

func TestServer_GetOrder_KernelAfterSubmit_ShouldReturnOrder(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-get")
	getRsp, err := h.GetOrder(ctx, &mooxpb.GetOrderReq{OrderId: "ord-get"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, getRsp.RetInfo.Code)
	assert.Equal(t, "ord-get", getRsp.Order.OrderId)
}

func TestServer_CancelOrder_KernelOpenOrder_ShouldCancel(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-cancel")
	rsp, err := h.CancelOrder(ctx, &mooxpb.CancelOrderReq{ChannelId: "chan-1", OrderId: "ord-cancel"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListOrders_Kernel_ShouldReturnOpenOrder(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-list")
	rsp, err := h.ListOrders(ctx, &mooxpb.ListOrdersReq{
		AccountId: "acct-1", OnlyOpen: true, Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.Orders)
}

func TestServer_ListTrades_KernelEmpty_ShouldSucceed(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-trades")
	rsp, err := h.ListTrades(ctx, &mooxpb.ListTradesReq{
		AccountId: "acct-1", Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	_ = engine
}
