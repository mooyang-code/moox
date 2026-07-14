package dao

import (
	"context"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNotDeletedUsesBoolSoftDelete(t *testing.T) {
	if got := notDeleted(); got != "c_is_deleted = 0" {
		t.Fatalf("notDeleted() = %q, want bool soft-delete predicate", got)
	}
}

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
	var stored service.APIKey
	require.NoError(t, store.db.Where("c_api_key_id = ?", "key_1").First(&stored).Error)
	assert.NotEqual(t, key.APIKey, stored.APIKey)
	assert.NotEqual(t, key.APISecret, stored.APISecret)
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
