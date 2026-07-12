package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/mooyang-code/moox/modules/trade/internal/service/dao"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradeschema "github.com/mooyang-code/moox/modules/trade/schema"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"gorm.io/gorm"
)

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
	createRsp, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "main", BaseCurrency: "USDT"})
	require.NoError(t, err)
	require.Equal(t, mooxpb.ErrorCode_SUCCESS, createRsp.RetInfo.Code)
	assert.NotEmpty(t, createRsp.AccountId)

	getRsp, err := h.GetAccount(ctx, &mooxpb.GetAccountReq{AccountId: createRsp.AccountId})
	require.NoError(t, err)
	assert.Equal(t, "main", getRsp.Account.AccountName)
}

func TestServer_ListAccounts_ShouldReturnCreatedAccount(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "list-me"})
	require.NoError(t, err)
	rsp, err := h.ListAccounts(ctx, &mooxpb.ListAccountsReq{UserId: "user-1", Page: &mooxpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.GreaterOrEqual(t, len(rsp.Accounts), 1)
}

func TestServer_UpdateAndDeleteAccount_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	createRsp, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "before"})
	require.NoError(t, err)
	_, err = h.UpdateAccount(ctx, &mooxpb.UpdateAccountReq{AccountId: createRsp.AccountId, AccountName: "after"})
	require.NoError(t, err)
	_, err = h.DeleteAccount(ctx, &mooxpb.DeleteAccountReq{AccountId: createRsp.AccountId})
	require.NoError(t, err)
	getRsp, err := h.GetAccount(ctx, &mooxpb.GetAccountReq{AccountId: createRsp.AccountId})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_NOT_FOUND, getRsp.RetInfo.Code)
}

func TestServer_CreateChannel_ShouldPersist(t *testing.T) {
	svc, _ := newRPCTestService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	createAcc, err := New(svc).CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "acc"})
	require.NoError(t, err)
	h := New(svc)
	rsp, err := h.CreateChannel(ctx, &mooxpb.CreateChannelReq{
		ChannelName: "binance-spot", Exchange: "binance", AccountId: createAcc.AccountId,
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.ChannelId)
}

func TestServer_GetBalances_NoKernel_ShouldUseServiceStore(t *testing.T) {
	svc, _ := newRPCTestService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	h := New(svc)
	acc, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "bal"})
	require.NoError(t, err)
	require.NoError(t, svc.Account.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: acc.AccountId, Currency: "USDT", Available: "50", Frozen: "5", Total: "55",
	}}))
	rsp, err := h.GetBalances(ctx, &mooxpb.GetBalancesReq{AccountId: acc.AccountId})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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
	rsp, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "x"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}
