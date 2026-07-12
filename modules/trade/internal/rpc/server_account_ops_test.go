package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/instrument"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type dustStubAdapter struct {
	rpcStubAdapter
}

func (a *dustStubAdapter) ListConvertibleDustAssets(context.Context, exchange.Credential, *exchange.DustConvertibleReq) ([]exchange.DustConvertibleAsset, error) {
	return []exchange.DustConvertibleAsset{{Asset: "GALA"}}, nil
}

func (a *dustStubAdapter) ConvertDust(context.Context, exchange.Credential, *exchange.DustTransferReq) (*exchange.DustTransferResult, error) {
	return &exchange.DustTransferResult{
		TotalTransfered: "0.01", TotalServiceCharge: "0.001",
		Results: []exchange.DustTransferItem{{Asset: "GALA", Amount: "100", TransferedAmount: "0.01"}},
	}, nil
}

func (a *dustStubAdapter) GetBalances(context.Context, exchange.Credential, exchange.MarketType, []string) ([]exchange.Balance, error) {
	return []exchange.Balance{{Currency: "USDT", Available: "500", Frozen: "0", Total: "500"}}, nil
}

func newDustRPCService(t *testing.T) (*service.Service, *Server) {
	t.Helper()
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return &dustStubAdapter{}, nil
	}))
	return svc, New(svc)
}

func TestServer_SyncBalances_ServiceOnly_ShouldReturnBalances(t *testing.T) {
	svc, h := newDustRPCService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SyncBalances(ctx, &mooxpb.SyncBalancesReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Balances, 1)
	assert.Equal(t, "500", rsp.Balances[0].Available)
}

func TestServer_SyncBalances_WithKernel_ShouldReconcileProjections(t *testing.T) {
	svc, _ := newDustRPCService(t)
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	h := New(svc, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SyncBalances(ctx, &mooxpb.SyncBalancesReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ConvertDust_WithEligibleAssets_ShouldSucceed(t *testing.T) {
	svc, h := newDustRPCService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.ConvertDust(ctx, &mooxpb.ConvertDustReq{
		ChannelId: channelID, AccountId: accountID, Assets: []string{"GALA"},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "0.01", rsp.TotalTransfered)
	require.Len(t, rsp.Results, 1)
}

func TestServer_SetLeverage_ValidChannel_ShouldSucceed(t *testing.T) {
	svc, h := newDustRPCService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SetLeverage(ctx, &mooxpb.SetLeverageReq{
		ChannelId: channelID, Symbol: "BTCUSDT", Leverage: "10",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_AmendOrder_ServicePath_ShouldUpdatePrice(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	require.NoError(t, svc.Account.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: accountID, Currency: "USDT", Available: "1000", Total: "1000",
	}}))
	placeRsp, err := h.PlaceOrder(ctx, &mooxpb.PlaceOrderReq{
		AccountId: accountID, ChannelId: channelID, Symbol: "BTCUSDT",
		Side: mooxpb.OrderSide_ORDER_SIDE_BUY, OrderType: mooxpb.OrderType_ORDER_TYPE_LIMIT,
		Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, placeRsp.RetInfo.Code)
	rsp, err := h.AmendOrder(ctx, &mooxpb.AmendOrderReq{
		ChannelId: channelID, OrderId: placeRsp.OrderId, NewPrice: "110",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_Transfer_BetweenAccounts_ShouldSucceed(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	fromRsp, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "from"})
	require.NoError(t, err)
	toRsp, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "to"})
	require.NoError(t, err)
	rsp, err := h.Transfer(ctx, &mooxpb.TransferReq{
		FromAccountId: fromRsp.AccountId, ToAccountId: toRsp.AccountId,
		Currency: "USDT", Amount: "10", Remark: "move",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.OutFlowId)
	assert.NotEmpty(t, rsp.InFlowId)
}

func TestServer_AmendOrder_KernelPath_OpenOrder_ShouldReplace(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	seedKernelBalance(t, ks)
	engine := &command.Engine{Store: ks, Adapter: amendStubAdapter{}}
	ctx := rpcCtx(t, "crypto", "user-1")
	placed, err := engine.Place(ctx, command.PlaceInput{
		SpaceID: "crypto", OrderID: "ord-amend", ClientOrderID: "cli-amend",
		AccountID: "acct-1", ChannelID: "chan-1", Symbol: "BTC-USDT",
		MarketType: "spot", BaseAsset: "BTC", QuoteAsset: "USDT",
		Side: "BUY", Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	_, err = engine.Submit(ctx, "crypto", placed.OrderID, "")
	require.NoError(t, err)
	h := New(nil, engine)
	rsp, err := h.AmendOrder(ctx, &mooxpb.AmendOrderReq{
		ChannelId: "chan-1", OrderId: placed.OrderID, NewPrice: "99",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

type amendStubAdapter struct{}

func (amendStubAdapter) Place(context.Context, exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-amend", Status: "OPEN"}, nil
}
func (amendStubAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (amendStubAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "OPEN"}, nil
}
func (amendStubAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (amendStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (amendStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}
