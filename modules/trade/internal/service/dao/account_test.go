package dao

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccountCRUD_Lifecycle_ShouldPersist(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	account := &service.Account{
		AccountID: "acc_1", UserID: "user_1", AccountName: "main",
		AccountType: service.AccountSpot, BaseCurrency: "USDT", Status: service.AccountNormal,
	}
	require.NoError(t, store.CreateAccount(ctx, "crypto", account))

	got, err := store.GetAccount(ctx, "crypto", "acc_1")
	require.NoError(t, err)
	assert.Equal(t, "main", got.AccountName)

	account.AccountName = "updated"
	require.NoError(t, store.UpdateAccount(ctx, "crypto", account))
	got, err = store.GetAccount(ctx, "crypto", "acc_1")
	require.NoError(t, err)
	assert.Equal(t, "updated", got.AccountName)

	list, total, err := store.ListAccounts(ctx, "crypto", service.AccountFilter{UserID: "user_1"}, service.Page{PageNo: 1, PageSize: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, list, 1)

	require.NoError(t, store.DeleteAccount(ctx, "crypto", "acc_1"))
	_, err = store.GetAccount(ctx, "crypto", "acc_1")
	assert.ErrorIs(t, err, service.ErrNotFound)
}

func TestBalanceUpsertAndGet_ShouldRoundTrip(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateAccount(ctx, "crypto", &service.Account{
		AccountID: "acc_1", UserID: "user_1", AccountName: "main", AccountType: service.AccountSpot,
	}))
	balances := []*service.Balance{{
		AccountID: "acc_1", Currency: "USDT", Available: "100", Frozen: "10", Total: "110",
	}}
	require.NoError(t, store.UpsertBalances(ctx, "crypto", balances))
	got, err := store.GetBalances(ctx, "crypto", "acc_1", nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "100", got[0].Available)
}

func TestChannelCRUD_Lifecycle_ShouldPersist(t *testing.T) {
	store := newSyncCursorTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateAccount(ctx, "crypto", &service.Account{
		AccountID: "acc_1", UserID: "user_1", AccountName: "main", AccountType: service.AccountSpot,
	}))
	ch := &service.TradeChannel{
		ChannelID: "ch_1", ChannelName: "binance-spot", Exchange: "binance",
		MarketType: "spot", AccountID: "acc_1", Status: 1,
	}
	require.NoError(t, store.CreateChannel(ctx, "crypto", ch))
	got, err := store.GetChannel(ctx, "crypto", "ch_1")
	require.NoError(t, err)
	assert.Equal(t, "binance-spot", got.ChannelName)

	ch.ChannelName = "updated"
	require.NoError(t, store.UpdateChannel(ctx, "crypto", ch))
	got, err = store.GetChannel(ctx, "crypto", "ch_1")
	require.NoError(t, err)
	assert.Equal(t, "updated", got.ChannelName)
}
