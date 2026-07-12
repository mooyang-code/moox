package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	rsp, err := h.PlaceOrder(ctx, &mooxpb.PlaceOrderReq{
		AccountId: "acct-1", ChannelId: "chan-1", Symbol: "BTC-USDT",
		Side: mooxpb.OrderSide_ORDER_SIDE_BUY, Quantity: "1", Price: "100",
		MarketType: mooxpb.MarketType_MARKET_TYPE_SPOT,
	})
	require.NoError(t, err)
	assert.NotEqual(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Contains(t, rsp.RetInfo.Msg, "instrument assets required")
}

func TestServer_CancelAllOrders_WithKernel_NoOpenOrders_ShouldReturnZero(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	engine := &command.Engine{Store: ks}
	h := New(nil, engine)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.CancelAllOrders(ctx, &mooxpb.CancelAllOrdersReq{ChannelId: "chan-1"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.Equal(t, int32(0), rsp.CanceledCount)
}

func TestServer_GetBalances_WithKernel_ShouldReadProjections(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	seedKernelBalance(t, ks)
	h := New(nil, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.GetBalances(ctx, &mooxpb.GetBalancesReq{AccountId: "acct-1"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
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
	acc, err := h.CreateAccount(ctx, &mooxpb.CreateAccountReq{AccountName: "sync-bal"})
	require.NoError(t, err)
	require.NoError(t, storeDAO.UpsertBalances(ctx, "crypto", []*service.Balance{{
		AccountID: acc.AccountId, Currency: "USDT", Available: "10", Frozen: "0", Total: "10",
	}}))
	rsp, err := h.SyncBalances(ctx, &mooxpb.SyncBalancesReq{AccountId: acc.AccountId})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}
