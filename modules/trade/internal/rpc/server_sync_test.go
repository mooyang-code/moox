package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedLinkedAccountChannel(t *testing.T, svc *service.Service, h *Server, ctx context.Context) (accountID, channelID string) {
	t.Helper()
	accountID, channelID = seedAccountChannel(t, h, ctx)
	apiRsp, err := h.CreateApiKey(ctx, &mooxpb.CreateApiKeyReq{
		AccountId: accountID, Exchange: "binance", ApiKey: "k", ApiSecret: "secret12",
	})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, apiRsp.RetInfo.Code)
	_, err = h.DeleteChannel(ctx, &mooxpb.DeleteChannelReq{ChannelId: channelID})
	require.NoError(t, err)
	chRsp, err := h.CreateChannel(ctx, &mooxpb.CreateChannelReq{
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

	rsp, err := h.SyncOrders(ctx, &mooxpb.SyncOrdersReq{
		AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncTrades_WithStubAdapter_ShouldSucceed(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SyncTrades(ctx, &mooxpb.SyncTradesReq{
		AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncPositions_WithStubAdapter_ShouldSucceed(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.SyncPositions(ctx, &mooxpb.SyncPositionsReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListOrders_Empty_ShouldSucceed(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListOrders(ctx, &mooxpb.ListOrdersReq{
		AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListPositions_Empty_ShouldSucceed(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListPositions(ctx, &mooxpb.ListPositionsReq{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncExchangeAccounts_WithoutSecretSource_ShouldReject(t *testing.T) {
	_, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.SyncExchangeAccounts(ctx, &mooxpb.SyncExchangeAccountsReq{})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}
