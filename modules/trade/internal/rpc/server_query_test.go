package rpc

import (
	"context"
	"testing"

	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedAccountChannel(t *testing.T, h *Server, ctx context.Context) (accountID, channelID string) {
	t.Helper()
	acc, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "seed"})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, acc.RetInfo.Code)
	ch, err := h.CreateChannel(ctx, &mooxpb.CreateChannelReq{
		ChannelName: "seed-ch", Exchange: "binance", AccountId: acc.AccountId,
	})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, ch.RetInfo.Code)
	return acc.AccountId, ch.ChannelId
}

func TestServer_ListFundFlows_ShouldReturnEmpty(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListFundFlows(ctx, &mooxpb.ListFundFlowsReq{AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Empty(t, rsp.Flows)
}

func TestServer_ListApiKeys_AfterCreate_ShouldReturnMaskedKey(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	createRsp, err := h.CreateApiKey(ctx, &mooxpb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "plain-key", ApiSecret: "plain-secret",
	})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, createRsp.RetInfo.Code)
	listRsp, err := h.ListApiKeys(ctx, &mooxpb.ListApiKeysReq{AccountId: accountID})
	require.NoError(t, err)
	require.Len(t, listRsp.ApiKeys, 1)
	assert.NotEqual(t, "plain-key", listRsp.ApiKeys[0].ApiKey)
}

func TestServer_ListChannels_ShouldReturnCreatedChannel(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListChannels(ctx, &mooxpb.ListChannelsReq{Page: &mooxpb.Page{Page: 1, Size: 10}})
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
	rsp, err := h.ListOrders(ctx, &mooxpb.ListOrdersReq{AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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
	rsp, err := h.GetOrder(ctx, &mooxpb.GetOrderReq{OrderId: "ord-2"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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
	rsp, err := h.ListPositions(ctx, &mooxpb.ListPositionsReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Positions, 1)
}

func TestServer_Transfer_InvalidParams_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.Transfer(ctx, &mooxpb.TransferReq{FromAccountId: "", ToAccountId: "b", Currency: "USDT", Amount: "1"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_DeleteApiKey_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	createRsp, err := h.CreateApiKey(ctx, &mooxpb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "k", ApiSecret: "s",
	})
	require.NoError(t, err)
	delRsp, err := h.DeleteApiKey(ctx, &mooxpb.DeleteApiKeyReq{ApiKeyId: createRsp.ApiKeyId})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, delRsp.RetInfo.Code)
}
