package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rpcStubAdapter struct {
	placeResult *exchange.OrderResult
}

func (a *rpcStubAdapter) Name() string { return "stub" }
func (a *rpcStubAdapter) Ping(context.Context, exchange.Credential) (int64, error) { return 0, nil }
func (a *rpcStubAdapter) GetInstruments(context.Context, exchange.MarketType) ([]exchange.Instrument, error) {
	return []exchange.Instrument{{Symbol: "BTCUSDT", LotSize: "0.001", MinQty: "0.001"}}, nil
}
func (a *rpcStubAdapter) GetAccountInfo(context.Context, exchange.Credential, exchange.MarketType) (*exchange.AccountInfo, error) {
	return nil, nil
}
func (a *rpcStubAdapter) GetBalances(context.Context, exchange.Credential, exchange.MarketType, []string) ([]exchange.Balance, error) {
	return nil, nil
}
func (a *rpcStubAdapter) GetTradeFee(context.Context, exchange.Credential, exchange.MarketType, string) (*exchange.FeeRate, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ListFundFlows(context.Context, exchange.Credential, *exchange.FundFlowQuery) ([]exchange.FundFlow, error) {
	return nil, nil
}
func (a *rpcStubAdapter) Transfer(context.Context, exchange.Credential, *exchange.TransferReq) (*exchange.TransferResult, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ListConvertibleDustAssets(context.Context, exchange.Credential, *exchange.DustConvertibleReq) ([]exchange.DustConvertibleAsset, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ConvertDust(context.Context, exchange.Credential, *exchange.DustTransferReq) (*exchange.DustTransferResult, error) {
	return nil, nil
}
func (a *rpcStubAdapter) PlaceOrder(context.Context, exchange.Credential, *exchange.PlaceOrderReq) (*exchange.OrderResult, error) {
	if a.placeResult != nil {
		return a.placeResult, nil
	}
	return &exchange.OrderResult{ExchangeOrderID: "ex-rpc", Status: exchange.StatusSubmitted}, nil
}
func (a *rpcStubAdapter) CancelOrder(context.Context, exchange.Credential, *exchange.CancelOrderReq) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{Status: exchange.StatusCanceled}, nil
}
func (a *rpcStubAdapter) CancelAllOrders(context.Context, exchange.Credential, exchange.MarketType, string) (int, error) {
	return 0, nil
}
func (a *rpcStubAdapter) AmendOrder(context.Context, exchange.Credential, *exchange.AmendOrderReq) (*exchange.OrderResult, error) {
	return &exchange.OrderResult{Status: exchange.StatusSubmitted}, nil
}
func (a *rpcStubAdapter) SetLeverage(context.Context, exchange.Credential, exchange.MarketType, string, string) error {
	return nil
}
func (a *rpcStubAdapter) ClosePosition(context.Context, exchange.Credential, exchange.MarketType, string, string) error {
	return nil
}
func (a *rpcStubAdapter) GetOrder(context.Context, exchange.Credential, *exchange.GetOrderReq) (*exchange.Order, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ListOpenOrders(context.Context, exchange.Credential, *exchange.ListOrdersReq) ([]exchange.Order, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ListOrders(context.Context, exchange.Credential, *exchange.ListOrdersReq) ([]exchange.Order, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ListTrades(context.Context, exchange.Credential, *exchange.ListTradesReq) ([]exchange.Trade, error) {
	return nil, nil
}
func (a *rpcStubAdapter) ListPositions(context.Context, exchange.Credential, exchange.MarketType, string) ([]exchange.Position, error) {
	return nil, nil
}

func newRPCStubService(t *testing.T) (*service.Service, *Server) {
	t.Helper()
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return &rpcStubAdapter{
			placeResult: &exchange.OrderResult{ExchangeOrderID: "ex-rpc", Status: exchange.StatusSubmitted},
		}, nil
	}))
	return svc, New(svc)
}

func TestServer_PlaceOrder_InvalidChannel_ShouldReject(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.PlaceOrder(ctx, &mooxpb.PlaceOrderReq{
		AccountId: accountID, ChannelId: "missing-ch", Symbol: "BTCUSDT",
		Side: mooxpb.OrderSide_ORDER_SIDE_BUY, OrderType: mooxpb.OrderType_ORDER_TYPE_LIMIT,
		Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	assert.NotEqual(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_PlaceOrder_ValidRequest_ShouldSucceed(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accRsp, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "trade-acc"})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, accRsp.RetInfo.Code)
	accountID := accRsp.AccountId
	require.NoError(t, svc.Account.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: accountID, Currency: "USDT", Available: "1000", Total: "1000",
	}}))
	apiRsp, err := h.CreateApiKey(ctx, &mooxpb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "k", ApiSecret: "secret12",
	})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, apiRsp.RetInfo.Code)
	chRsp, err := h.CreateChannel(ctx, &mooxpb.CreateChannelReq{
		ChannelName: "linked", Exchange: "binance", AccountId: accountID, ApiKeyId: apiRsp.ApiKeyId,
	})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, chRsp.RetInfo.Code)
	channelID := chRsp.ChannelId

	rsp, err := h.PlaceOrder(ctx, &mooxpb.PlaceOrderReq{
		AccountId: accountID, ChannelId: channelID, Symbol: "BTCUSDT",
		Side: mooxpb.OrderSide_ORDER_SIDE_BUY, OrderType: mooxpb.OrderType_ORDER_TYPE_LIMIT,
		Quantity: "1", Price: "100", ClientOrderId: "rpc-cli-1",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.OrderId)
}

func TestServer_CancelOrder_MissingOrder_ShouldFail(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.CancelOrder(ctx, &mooxpb.CancelOrderReq{
		ChannelId: channelID, OrderId: "missing",
	})
	require.NoError(t, err)
	assert.NotEqual(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_AmendOrder_InvalidParams_ShouldReject(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.AmendOrder(ctx, &mooxpb.AmendOrderReq{})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_CancelAllOrders_ValidChannel_ShouldReturnCount(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.CancelAllOrders(ctx, &mooxpb.CancelAllOrdersReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}
