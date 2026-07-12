package rpc

import (
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_ListFundFlows_WithStoredFlows_ShouldReturnItems(t *testing.T) {
	svc, store := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	require.NoError(t, store.AppendFundFlows(ctx, "crypto", []*service.FundFlow{{
		FlowID: "flow-db", AccountID: accountID, Currency: "USDT", BizType: "transfer",
		Direction: 1, Amount: "5", BalanceAfter: "95",
	}}))
	rsp, err := h.ListFundFlows(ctx, &mooxpb.ListFundFlowsReq{
		AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.Len(t, rsp.Flows, 1)
	assert.Equal(t, "flow-db", rsp.Flows[0].FlowId)
}

func TestServer_Transfer_ValidAccounts_ShouldCreateFlows(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
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

func TestServer_ListFundFlows_EmptyAccount_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.ListFundFlows(ctx, &mooxpb.ListFundFlowsReq{Page: &mooxpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}
