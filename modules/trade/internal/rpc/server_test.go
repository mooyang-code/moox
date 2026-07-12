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
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/mooyang-code/moox/modules/trade/internal/application/consumer"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"net/http"
	"net/http/httptest"
	"github.com/mooyang-code/moox/modules/trade/internal/service/dao"
	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/glebarez/sqlite"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"gorm.io/gorm"
	"errors"
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
	rsp, err := h.SyncBalances(ctx, &tradepb.SyncBalancesReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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
	rsp, err := h.SyncBalances(ctx, &tradepb.SyncBalancesReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ConvertDust_WithEligibleAssets_ShouldSucceed(t *testing.T) {
	svc, h := newDustRPCService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.ConvertDust(ctx, &tradepb.ConvertDustReq{
		ChannelId: channelID, AccountId: accountID, Assets: []string{"GALA"},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "0.01", rsp.TotalTransfered)
	require.Len(t, rsp.Results, 1)
}

func TestServer_SetLeverage_ValidChannel_ShouldSucceed(t *testing.T) {
	svc, h := newDustRPCService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SetLeverage(ctx, &tradepb.SetLeverageReq{
		ChannelId: channelID, Symbol: "BTCUSDT", Leverage: "10",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_AmendOrder_ServicePath_ShouldUpdatePrice(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	require.NoError(t, svc.Account.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: accountID, Currency: "USDT", Available: "1000", Total: "1000",
	}}))
	placeRsp, err := h.PlaceOrder(ctx, &tradepb.PlaceOrderReq{
		AccountId: accountID, ChannelId: channelID, Symbol: "BTCUSDT",
		Side: tradepb.OrderSide_ORDER_SIDE_BUY, OrderType: tradepb.OrderType_ORDER_TYPE_LIMIT,
		Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, placeRsp.RetInfo.Code)
	rsp, err := h.AmendOrder(ctx, &tradepb.AmendOrderReq{
		ChannelId: channelID, OrderId: placeRsp.OrderId, NewPrice: "110",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_Transfer_BetweenAccounts_ShouldSucceed(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	fromRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "from"})
	require.NoError(t, err)
	toRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "to"})
	require.NoError(t, err)
	rsp, err := h.Transfer(ctx, &tradepb.TransferReq{
		FromAccountId: fromRsp.AccountId, ToAccountId: toRsp.AccountId,
		Currency: "USDT", Amount: "10", Remark: "move",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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
	rsp, err := h.AmendOrder(ctx, &tradepb.AmendOrderReq{
		ChannelId: "chan-1", OrderId: placed.OrderID, NewPrice: "99",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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

type pingStubAdapter struct {
	rpcStubAdapter
	latency int64
	pingErr error
}

func (a *pingStubAdapter) Ping(context.Context, exchange.Credential) (int64, error) {
	if a.pingErr != nil {
		return 0, a.pingErr
	}
	return a.latency, nil
}

func newPingRPCService(t *testing.T, latency int64, pingErr error) (*Server, string) {
	t.Helper()
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return &pingStubAdapter{latency: latency, pingErr: pingErr}, nil
	}))
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	return h, channelID
}

func TestServer_TestChannel_Reachable_ShouldReturnLatency(t *testing.T) {
	h, channelID := newPingRPCService(t, 42, nil)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.TestChannel(ctx, &tradepb.TestChannelReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.True(t, rsp.Reachable)
	assert.Equal(t, int32(42), rsp.LatencyMs)
}

func TestServer_TestChannel_MissingChannel_ShouldReturnError(t *testing.T) {
	h, _ := newPingRPCService(t, 0, nil)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.TestChannel(ctx, &tradepb.TestChannelReq{ChannelId: "missing"})
	require.NoError(t, err)
	assert.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListChannels_WithFilters_ShouldReturnChannel(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.ListChannels(ctx, &tradepb.ListChannelsReq{
		AccountId: accountID, Exchange: "binance", Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.NotEmpty(t, rsp.Channels)
	assert.Equal(t, channelID, rsp.Channels[0].ChannelId)
}

func TestServer_ListFundFlows_WithStoredFlows_ShouldReturnItems(t *testing.T) {
	svc, store := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	require.NoError(t, store.AppendFundFlows(ctx, "crypto", []*service.FundFlow{{
		FlowID: "flow-db", AccountID: accountID, Currency: "USDT", BizType: "transfer",
		Direction: 1, Amount: "5", BalanceAfter: "95",
	}}))
	rsp, err := h.ListFundFlows(ctx, &tradepb.ListFundFlowsReq{
		AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Flows, 1)
	assert.Equal(t, "flow-db", rsp.Flows[0].FlowId)
}

func TestServer_Transfer_ValidAccounts_ShouldCreateFlows(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	fromRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "from"})
	require.NoError(t, err)
	toRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "to"})
	require.NoError(t, err)
	rsp, err := h.Transfer(ctx, &tradepb.TransferReq{
		FromAccountId: fromRsp.AccountId, ToAccountId: toRsp.AccountId,
		Currency: "USDT", Amount: "10", Remark: "move",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.OutFlowId)
	assert.NotEmpty(t, rsp.InFlowId)
}

func TestServer_ListFundFlows_EmptyAccount_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.ListFundFlows(ctx, &tradepb.ListFundFlowsReq{Page: &tradepb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

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
	getRsp, err := h.GetOrder(ctx, &tradepb.GetOrderReq{OrderId: "ord-get"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, getRsp.RetInfo.Code)
	assert.Equal(t, "ord-get", getRsp.Order.OrderId)
}

func TestServer_CancelOrder_KernelOpenOrder_ShouldCancel(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-cancel")
	rsp, err := h.CancelOrder(ctx, &tradepb.CancelOrderReq{ChannelId: "chan-1", OrderId: "ord-cancel"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListOrders_Kernel_ShouldReturnOpenOrder(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-list")
	rsp, err := h.ListOrders(ctx, &tradepb.ListOrdersReq{
		AccountId: "acct-1", OnlyOpen: true, Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.Orders)
}

func TestServer_ListTrades_KernelEmpty_ShouldSucceed(t *testing.T) {
	h, engine := newKernelTradeServer(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	seedKernelOpenOrder(t, engine, "ord-trades")
	rsp, err := h.ListTrades(ctx, &tradepb.ListTradesReq{
		AccountId: "acct-1", Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	_ = engine
}

func openKernelStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestServer_SetPause_InvalidTargetType_ShouldReject(t *testing.T) {
	h := New(nil, &command.Engine{Store: openKernelStore(t)})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.SetPause(ctx, &tradepb.SetTradePauseReq{TargetType: "invalid", TargetId: "x"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_SetPause_ValidAccount_ShouldPersist(t *testing.T) {
	ks := openKernelStore(t)
	h := New(nil, &command.Engine{Store: ks})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.SetPause(ctx, &tradepb.SetTradePauseReq{TargetType: "account", TargetId: "acc-1", Paused: true, Reason: "test"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	paused, err := ks.IsPaused(ctx, "space-1", "acc-1", "")
	require.NoError(t, err)
	assert.True(t, paused)
}

func TestServer_ReconcileNow_WithKernel_ShouldEnqueueOutbox(t *testing.T) {
	ks := openKernelStore(t)
	h := New(nil, &command.Engine{Store: ks})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.ReconcileNow(ctx, &tradepb.ReconcileNowReq{AccountId: "acc-1", ChannelId: "ch-1"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.MessageId)
}

func TestServer_InspectSaga_MissingSaga_ShouldReturnNotFound(t *testing.T) {
	h := New(nil, &command.Engine{Store: openKernelStore(t)})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.InspectSaga(ctx, &tradepb.InspectSagaReq{SagaId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func TestServer_KernelNil_ShouldReturnInnerError(t *testing.T) {
	h := New(nil)
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.SetPause(ctx, &tradepb.SetTradePauseReq{TargetType: "account", TargetId: "a"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func seedKernelBalance(t *testing.T, s *store.Store) {
	t.Helper()
	require.NoError(t, s.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger("crypto", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed"), BizType: "seed", RefType: "test", RefID: "1",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "USDT", Bucket: "clearing", Amount: shared.MustDecimal("1000").Neg()},
				{AccountID: "acct-1", Asset: "USDT", Bucket: "available", Amount: shared.MustDecimal("1000")},
			},
		})
	}))
}

func TestServer_PlaceOrder_WithKernel_MissingAssets_ShouldReject(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	seedKernelBalance(t, ks)
	h := New(nil, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.PlaceOrder(ctx, &tradepb.PlaceOrderReq{
		AccountId: "acct-1", ChannelId: "chan-1", Symbol: "BTC-USDT",
		Side: tradepb.OrderSide_ORDER_SIDE_BUY, Quantity: "1", Price: "100",
		MarketType: tradepb.MarketType_MARKET_TYPE_SPOT,
	})
	require.NoError(t, err)
	assert.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Contains(t, rsp.RetInfo.Msg, "instrument assets required")
}

func TestServer_CancelAllOrders_WithKernel_NoOpenOrders_ShouldReturnZero(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	engine := &command.Engine{Store: ks}
	h := New(nil, engine)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.CancelAllOrders(ctx, &tradepb.CancelAllOrdersReq{ChannelId: "chan-1"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, int32(0), rsp.CanceledCount)
}

func TestServer_GetBalances_WithKernel_ShouldReadProjections(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	seedKernelBalance(t, ks)
	h := New(nil, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.GetBalances(ctx, &tradepb.GetBalancesReq{AccountId: "acct-1"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Balances, 1)
	assert.Equal(t, "1000", rsp.Balances[0].Available)
}

func TestServer_SyncBalances_WithKernel_ShouldDelegateService(t *testing.T) {
	svc, storeDAO := newRPCTestService(t)
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	h := New(svc, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	acc, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "sync-bal"})
	require.NoError(t, err)
	require.NoError(t, storeDAO.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: acc.AccountId, Currency: "USDT", Available: "10", Frozen: "0", Total: "10",
	}}))
	rsp, err := h.SyncBalances(ctx, &tradepb.SyncBalancesReq{AccountId: acc.AccountId})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

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
	rsp, err := h.PlaceOrder(ctx, &tradepb.PlaceOrderReq{
		AccountId: accountID, ChannelId: "missing-ch", Symbol: "BTCUSDT",
		Side: tradepb.OrderSide_ORDER_SIDE_BUY, OrderType: tradepb.OrderType_ORDER_TYPE_LIMIT,
		Quantity: "1", Price: "100",
	})
	require.NoError(t, err)
	assert.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_PlaceOrder_ValidRequest_ShouldSucceed(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "trade-acc"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, accRsp.RetInfo.Code)
	accountID := accRsp.AccountId
	require.NoError(t, svc.Account.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: accountID, Currency: "USDT", Available: "1000", Total: "1000",
	}}))
	apiRsp, err := h.CreateApiKey(ctx, &tradepb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "k", ApiSecret: "secret12",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, apiRsp.RetInfo.Code)
	chRsp, err := h.CreateChannel(ctx, &tradepb.CreateChannelReq{
		ChannelName: "linked", Exchange: "binance", AccountId: accountID, ApiKeyId: apiRsp.ApiKeyId,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, chRsp.RetInfo.Code)
	channelID := chRsp.ChannelId

	rsp, err := h.PlaceOrder(ctx, &tradepb.PlaceOrderReq{
		AccountId: accountID, ChannelId: channelID, Symbol: "BTCUSDT",
		Side: tradepb.OrderSide_ORDER_SIDE_BUY, OrderType: tradepb.OrderType_ORDER_TYPE_LIMIT,
		Quantity: "1", Price: "100", ClientOrderId: "rpc-cli-1",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.OrderId)
}

func TestServer_CancelOrder_MissingOrder_ShouldFail(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.CancelOrder(ctx, &tradepb.CancelOrderReq{
		ChannelId: channelID, OrderId: "missing",
	})
	require.NoError(t, err)
	assert.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_AmendOrder_InvalidParams_ShouldReject(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.AmendOrder(ctx, &tradepb.AmendOrderReq{})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_CancelAllOrders_ValidChannel_ShouldReturnCount(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.CancelAllOrders(ctx, &tradepb.CancelAllOrdersReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func seedAccountChannel(t *testing.T, h *Server, ctx context.Context) (accountID, channelID string) {
	t.Helper()
	acc, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "seed"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, acc.RetInfo.Code)
	ch, err := h.CreateChannel(ctx, &tradepb.CreateChannelReq{
		ChannelName: "seed-ch", Exchange: "binance", AccountId: acc.AccountId,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, ch.RetInfo.Code)
	return acc.AccountId, ch.ChannelId
}

func TestServer_ListFundFlows_ShouldReturnEmpty(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListFundFlows(ctx, &tradepb.ListFundFlowsReq{AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Empty(t, rsp.Flows)
}

func TestServer_ListApiKeys_AfterCreate_ShouldReturnMaskedKey(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	createRsp, err := h.CreateApiKey(ctx, &tradepb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "plain-key", ApiSecret: "plain-secret",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, createRsp.RetInfo.Code)
	listRsp, err := h.ListApiKeys(ctx, &tradepb.ListApiKeysReq{AccountId: accountID})
	require.NoError(t, err)
	require.Len(t, listRsp.ApiKeys, 1)
	assert.NotEqual(t, "plain-key", listRsp.ApiKeys[0].ApiKey)
}

func TestServer_ListChannels_ShouldReturnCreatedChannel(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListChannels(ctx, &tradepb.ListChannelsReq{Page: &tradepb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	require.NotEmpty(t, rsp.Channels)
	assert.Equal(t, channelID, rsp.Channels[0].ChannelId)
}

func TestServer_ListOrders_AfterSave_ShouldReturnOrder(t *testing.T) {
	svc, store := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedAccountChannel(t, h, ctx)
	require.NoError(t, store.SaveOrder(ctx, "crypto", &service.Order{
		OrderID: "ord-1", ClientOrderID: "client-1", AccountID: accountID, ChannelID: channelID,
		Exchange: "binance", Symbol: "BTCUSDT", MarketType: "spot", Side: "buy", OrderType: "limit", Status: 1,
	}))
	rsp, err := h.ListOrders(ctx, &tradepb.ListOrdersReq{AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Orders, 1)
	assert.Equal(t, "ord-1", rsp.Orders[0].OrderId)
}

func TestServer_GetOrder_ShouldReturnSavedOrder(t *testing.T) {
	svc, store := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedAccountChannel(t, h, ctx)
	require.NoError(t, store.SaveOrder(ctx, "crypto", &service.Order{
		OrderID: "ord-2", ClientOrderID: "client-2", AccountID: accountID, ChannelID: channelID,
		Exchange: "binance", Symbol: "ETHUSDT", MarketType: "spot", Side: "sell", OrderType: "market", Status: 3,
	}))
	rsp, err := h.GetOrder(ctx, &tradepb.GetOrderReq{OrderId: "ord-2"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "ETHUSDT", rsp.Order.Symbol)
}

func TestServer_ListPositions_AfterUpsert_ShouldReturnPosition(t *testing.T) {
	svc, store := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	require.NoError(t, store.UpsertPositions(ctx, "crypto", []*service.Position{{
		PositionID: "pos-1", AccountID: accountID, Symbol: "BTCUSDT", Quantity: "2", AvgPrice: "60000",
	}}))
	rsp, err := h.ListPositions(ctx, &tradepb.ListPositionsReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Positions, 1)
}

func TestServer_Transfer_InvalidParams_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.Transfer(ctx, &tradepb.TransferReq{FromAccountId: "", ToAccountId: "b", Currency: "USDT", Amount: "1"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_DeleteApiKey_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	createRsp, err := h.CreateApiKey(ctx, &tradepb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "k", ApiSecret: "s",
	})
	require.NoError(t, err)
	delRsp, err := h.DeleteApiKey(ctx, &tradepb.DeleteApiKeyReq{ApiKeyId: createRsp.ApiKeyId})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, delRsp.RetInfo.Code)
}

type rebalanceStubAdapter struct{}

func (rebalanceStubAdapter) Place(_ context.Context, r exchange.PlaceRequest) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{ExchangeOrderID: "ex-" + r.ClientOrderID, Status: "OPEN"}, nil
}
func (rebalanceStubAdapter) Cancel(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "CANCELED"}, nil
}
func (rebalanceStubAdapter) QueryByClientOrderID(context.Context, string, string) (exchange.ExchangeOrderResult, error) {
	return exchange.ExchangeOrderResult{Status: "OPEN"}, nil
}
func (rebalanceStubAdapter) Rules(context.Context, string) (instrument.Rules, error) {
	return instrument.Rules{BaseAsset: "BTC", QuoteAsset: "USDT"}, nil
}
func (rebalanceStubAdapter) ListFills(context.Context, string, string) ([]exchange.FillEvent, error) {
	return nil, nil
}
func (rebalanceStubAdapter) SubscribePrivate(context.Context, exchange.PrivateEventHandler) error {
	return nil
}

func seedRebalanceKernel(t *testing.T) (*store.Store, *command.Engine, *Server) {
	t.Helper()
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	require.NoError(t, ks.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.PostLedger("crypto", ledger.Transaction{
			ID: shared.LedgerTransactionID("seed-btc"), BizType: "seed", RefType: "test", RefID: "btc",
			Entries: []ledger.Entry{
				{AccountID: "clearing", Asset: "BTC", Bucket: "clearing", Amount: shared.MustDecimal("2").Neg()},
				{AccountID: "acct-1", Asset: "BTC", Bucket: "available", Amount: shared.MustDecimal("2")},
			},
		})
	}))
	engine := &command.Engine{Store: ks, Adapter: rebalanceStubAdapter{}}
	return ks, engine, New(nil, engine)
}

func TestServer_CreateRebalance_ValidPlan_ShouldSucceed(t *testing.T) {
	_, _, h := seedRebalanceKernel(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.CreateRebalance(ctx, &tradepb.CreateRebalanceReq{
		RunId: "run-1", IdempotencyKey: "idem-1", AccountId: "acct-1", ChannelId: "chan-1",
		MarketSnapshotId: "m1", PositionSnapshotId: "p1", RulesVersion: "r1",
		Targets: []*tradepb.TargetPosition{{Symbol: "BTCUSDT", Quantity: "0"}},
		Currents: []*tradepb.CurrentPosition{{Symbol: "BTCUSDT", Quantity: "2"}},
		Markets: []*tradepb.RebalanceMarket{{Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "PLANNED", rsp.Status)
}

func TestServer_AdvanceRebalance_PlannedRun_ShouldExecute(t *testing.T) {
	ks, engine, h := seedRebalanceKernel(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, err := h.CreateRebalance(ctx, &tradepb.CreateRebalanceReq{
		RunId: "run-adv", IdempotencyKey: "idem-adv", AccountId: "acct-1", ChannelId: "chan-1",
		MarketSnapshotId: "m1", PositionSnapshotId: "p1", RulesVersion: "r1",
		Targets: []*tradepb.TargetPosition{{Symbol: "BTCUSDT", Quantity: "0"}},
		Currents: []*tradepb.CurrentPosition{{Symbol: "BTCUSDT", Quantity: "2"}},
		Markets: []*tradepb.RebalanceMarket{{Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Price: "10"}},
	})
	require.NoError(t, err)
	rsp, err := h.AdvanceRebalance(ctx, &tradepb.AdvanceRebalanceReq{
		RunId: "run-adv", AccountId: "acct-1", ChannelId: "chan-1",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, "EXECUTING", rsp.Status)
	legs, err := ks.ListRebalanceLegs(ctx, "crypto", "run-adv")
	require.NoError(t, err)
	require.Len(t, legs, 1)
	_, err = (consumer.SubmissionWorker{Engine: engine}).Handle(ctx, "crypto", legs[0].PlanID)
	require.NoError(t, err)
}

func newRPCTestService(t *testing.T) (*service.Service, *dao.GormStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(tradeschema.AllSQL()).Error)
	store := dao.New(db, "0123456789abcdef0123456789abcdef")
	return service.New("trade", service.WithStore(store)), store
}

func rpcCtx(t *testing.T, spaceID, userID string) context.Context {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	if userID != "" {
		req.Header.Set("X-User-Id", userID)
	}
	ctx := thttp.WithHeader(context.Background(), &thttp.Header{Request: req})
	return spacecontext.WithSpaceID(ctx, spaceID)
}

func TestServer_CreateAndGetAccount_ShouldRoundTrip(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	createRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "main", BaseCurrency: "USDT"})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, createRsp.RetInfo.Code)
	assert.NotEmpty(t, createRsp.AccountId)

	getRsp, err := h.GetAccount(ctx, &tradepb.GetAccountReq{AccountId: createRsp.AccountId})
	require.NoError(t, err)
	assert.Equal(t, "main", getRsp.Account.AccountName)
}

func TestServer_ListAccounts_ShouldReturnCreatedAccount(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "list-me"})
	require.NoError(t, err)
	rsp, err := h.ListAccounts(ctx, &tradepb.ListAccountsReq{UserId: "user-1", Page: &tradepb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.GreaterOrEqual(t, len(rsp.Accounts), 1)
}

func TestServer_UpdateAndDeleteAccount_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	createRsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "before"})
	require.NoError(t, err)
	_, err = h.UpdateAccount(ctx, &tradepb.UpdateAccountReq{AccountId: createRsp.AccountId, AccountName: "after"})
	require.NoError(t, err)
	_, err = h.DeleteAccount(ctx, &tradepb.DeleteAccountReq{AccountId: createRsp.AccountId})
	require.NoError(t, err)
	getRsp, err := h.GetAccount(ctx, &tradepb.GetAccountReq{AccountId: createRsp.AccountId})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_NOT_FOUND, getRsp.RetInfo.Code)
}

func TestServer_CreateChannel_ShouldPersist(t *testing.T) {
	svc, _ := newRPCTestService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	createAcc, err := New(svc).CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "acc"})
	require.NoError(t, err)
	h := New(svc)
	rsp, err := h.CreateChannel(ctx, &tradepb.CreateChannelReq{
		ChannelName: "binance-spot", Exchange: "binance", AccountId: createAcc.AccountId,
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.ChannelId)
}

func TestServer_GetBalances_NoKernel_ShouldUseServiceStore(t *testing.T) {
	svc, _ := newRPCTestService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	h := New(svc)
	acc, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "bal"})
	require.NoError(t, err)
	require.NoError(t, svc.Account.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: acc.AccountId, Currency: "USDT", Available: "50", Frozen: "5", Total: "55",
	}}))
	rsp, err := h.GetBalances(ctx, &tradepb.GetBalancesReq{AccountId: acc.AccountId})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Balances, 1)
	assert.Equal(t, "50", rsp.Balances[0].Available)
}

func TestServer_ServiceHealth_ShouldReturnReady(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	got := h.svc.Health()
	assert.True(t, got.Ready)
	assert.Equal(t, "trade", got.Module)
}

func TestServer_CreateAccount_NoUserID_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := spacecontext.WithSpaceID(context.Background(), "crypto")
	rsp, err := h.CreateAccount(ctx, &tradepb.CreateAccountReq{AccountName: "x"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

type rpcPingAdapter struct {
	exchange.ExchangeAdapter
	latency int64
	err     error
}

func (a rpcPingAdapter) Name() string { return "fake" }
func (a rpcPingAdapter) Ping(context.Context, exchange.Credential) (int64, error) {
	return a.latency, a.err
}

func TestServer_ListTrades_Empty_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListTrades(ctx, &tradepb.ListTradesReq{AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_UpdateChannel_ShouldPersist(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.UpdateChannel(ctx, &tradepb.UpdateChannelReq{
		ChannelId: channelID, ChannelName: "updated",
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_DeleteChannel_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.DeleteChannel(ctx, &tradepb.DeleteChannelReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncOrders_EmptyAccount_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.SyncOrders(ctx, &tradepb.SyncOrdersReq{AccountId: ""})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_AdvanceRebalance_NoKernel_ShouldReturnInnerError(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.AdvanceRebalance(ctx, &tradepb.AdvanceRebalanceReq{RunId: "run-1"})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func TestServer_CreateRebalance_InvalidQuantity_ShouldReject(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	h := New(nil, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.CreateRebalance(ctx, &tradepb.CreateRebalanceReq{
		RunId: "run-1", AccountId: "acct-1", ChannelId: "ch-1",
		Targets: []*tradepb.TargetPosition{{Symbol: "BTCUSDT", Quantity: "bad"}},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func TestServer_ListInstruments_WithoutAPIKey_ShouldFail(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListInstruments(ctx, &tradepb.ListInstrumentsReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.NotEqual(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_TestChannel_WithInjectedAdapter_ShouldReturnReachability(t *testing.T) {
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) {
		return rpcPingAdapter{latency: 42}, nil
	}))
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)

	rsp, err := h.TestChannel(ctx, &tradepb.TestChannelReq{ChannelId: channelID})

	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.True(t, rsp.Reachable)
	assert.Equal(t, int32(42), rsp.LatencyMs)
}

func TestServer_TestChannel_AdapterError_ShouldReturnRetInfo(t *testing.T) {
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) {
		return rpcPingAdapter{err: errors.New("dial failed")}, nil
	}))
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)

	rsp, err := h.TestChannel(ctx, &tradepb.TestChannelReq{ChannelId: channelID})

	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
	assert.False(t, rsp.Reachable)
}

func seedLinkedAccountChannel(t *testing.T, svc *service.Service, h *Server, ctx context.Context) (accountID, channelID string) {
	t.Helper()
	accountID, channelID = seedAccountChannel(t, h, ctx)
	apiRsp, err := h.CreateApiKey(ctx, &tradepb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "k", ApiSecret: "secret12",
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, apiRsp.RetInfo.Code)
	_, err = h.DeleteChannel(ctx, &tradepb.DeleteChannelReq{ChannelId: channelID})
	require.NoError(t, err)
	chRsp, err := h.CreateChannel(ctx, &tradepb.CreateChannelReq{
		ChannelName: "sync-ch", Exchange: "binance", AccountId: accountID, ApiKeyId: apiRsp.ApiKeyId,
	})
	require.NoError(t, err)
	channelID = chRsp.ChannelId
	acc, err := svc.Account.GetAccount(ctx, "crypto", accountID)
	require.NoError(t, err)
	acc.ChannelID = channelID
	_, err = svc.Account.UpdateAccount(ctx, "crypto", acc)
	require.NoError(t, err)
	return accountID, channelID
}

func TestServer_SyncOrders_WithStubAdapter_ShouldReturnOrders(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)

	rsp, err := h.SyncOrders(ctx, &tradepb.SyncOrdersReq{
		AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncTrades_WithStubAdapter_ShouldSucceed(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SyncTrades(ctx, &tradepb.SyncTradesReq{
		AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncPositions_WithStubAdapter_ShouldSucceed(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SyncPositions(ctx, &tradepb.SyncPositionsReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListOrders_Empty_ShouldSucceed(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListOrders(ctx, &tradepb.ListOrdersReq{
		AccountId: accountID, Page: &tradepb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListPositions_Empty_ShouldSucceed(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListPositions(ctx, &tradepb.ListPositionsReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncExchangeAccounts_WithoutSecretSource_ShouldReject(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.SyncExchangeAccounts(ctx, &tradepb.SyncExchangeAccountsReq{})
	require.NoError(t, err)
	assert.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}
