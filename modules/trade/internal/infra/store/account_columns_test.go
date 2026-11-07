package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLastDustConvertAtIsItsOwnColumn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.CreateTradingAccount(testAccount()) }))

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.SetLastDustConvertAt("space-1", "account-1", 1_700_000_000_000)
	}))

	account, err := s.GetTradingAccountByID(ctx, "account-1")
	require.NoError(t, err)
	require.Equal(t, int64(1_700_000_000_000), account.LastDustConvertAt)
}

// 测试网一个账户只开一个市场。
func TestTestnetAccountCannotOpenBothMarkets(t *testing.T) {
	s := openTestStore(t)
	account := testAccount()
	account.ExecutionMode, account.Environment = "LIVE", "TESTNET"
	account.Markets, account.MarginMode = []string{"SPOT", "SWAP"}, "CROSS"

	err := s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(account) })
	require.Error(t, err)

	account.Environment = "PRODUCTION"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(account) }))
}
