package dao

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFundFlowAppendAndList_ShouldPersist(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateAccount(ctx, "crypto", &service.Account{
		AccountID: "acc_1", UserID: "user_1", AccountName: "main", AccountType: service.AccountSpot,
	}))
	flows := []*service.FundFlow{{
		FlowID: "flow_1", AccountID: "acc_1", Currency: "USDT", BizType: "transfer",
		Direction: 1, Amount: "10", BalanceAfter: "110",
	}}
	require.NoError(t, store.AppendFundFlows(ctx, "crypto", flows))
	list, total, err := store.ListFundFlows(ctx, "crypto", service.FundFlowFilter{AccountID: "acc_1"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, list, 1)
}

func TestAPIKeyCRUD_ShouldPersist(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateAccount(ctx, "crypto", &service.Account{
		AccountID: "acc_1", UserID: "user_1", AccountName: "main", AccountType: service.AccountSpot,
	}))
	key := &service.APIKey{APIKeyID: "key_1", AccountID: "acc_1", Exchange: "binance", APIKey: "k", APISecret: "secret", Status: 1}
	require.NoError(t, store.CreateAPIKey(ctx, "crypto", key))
	got, err := store.GetAPIKey(ctx, "crypto", "key_1")
	require.NoError(t, err)
	assert.Equal(t, "binance", got.Exchange)
	list, err := store.ListAPIKeys(ctx, "crypto", "acc_1")
	require.NoError(t, err)
	assert.Len(t, list, 1)
	require.NoError(t, store.DeleteAPIKey(ctx, "crypto", "key_1"))
	_, err = store.GetAPIKey(ctx, "crypto", "key_1")
	assert.ErrorIs(t, err, service.ErrNotFound)
}

func TestPositionUpsertAndList_ShouldPersist(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateAccount(ctx, "crypto", &service.Account{
		AccountID: "acc_1", UserID: "user_1", AccountName: "main", AccountType: service.AccountSwap,
	}))
	positions := []*service.Position{{
		PositionID: "pos_1", AccountID: "acc_1", Symbol: "BTCUSDT", Quantity: "1", AvgPrice: "50000",
	}}
	require.NoError(t, store.UpsertPositions(ctx, "crypto", positions))
	got, err := store.ListPositions(ctx, "crypto", "acc_1", "BTCUSDT")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "1", got[0].Quantity)
}
