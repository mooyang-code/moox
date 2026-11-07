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

// 币安测试网一个账户只开一个市场；OKX 模拟盘可以同时开两个。
func TestTestnetAccountCannotOpenBothMarkets(t *testing.T) {
	s := openTestStore(t)
	account := testAccount()
	account.ExecutionMode, account.Environment = "LIVE", "TESTNET"
	account.Markets, account.MarginMode = []string{"SPOT", "SWAP"}, "CROSS"

	err := s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(account) })
	require.Error(t, err)

	account.Environment = "PRODUCTION"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(account) }))

	okx := testAccount()
	okx.TradingAccountID, okx.Name, okx.CredentialSecretID = "okx-demo", "okx-demo", "secret-okx"
	okx.Exchange, okx.ExecutionMode, okx.Environment = "OKX", "LIVE", "TESTNET"
	okx.Markets, okx.MarginMode = []string{"SPOT", "SWAP"}, "CROSS"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(okx) }), "OKX 模拟盘一个账户可以同时开两个市场")
}

// 检测结果只能写给检测时用的那个密钥：账户换了密钥，旧密钥的结果写不进去。
func TestSetTradingAccountInspectionRequiresSameCredential(t *testing.T) {
	s := openTestStore(t)
	account := testAccount()
	account.ExecutionMode, account.Environment = "LIVE", "PRODUCTION"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(account) }))
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.SetTradingAccountInspection("space-1", "account-1", "secret-1", "BINANCE_STANDARD", `{"account_mode":"BINANCE_STANDARD"}`)
	}))
	err := s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.SetTradingAccountInspection("space-1", "account-1", "old-secret", "BINANCE_PORTFOLIO_MARGIN", `{"account_mode":"BINANCE_PORTFOLIO_MARGIN"}`)
	})
	require.ErrorIs(t, err, ErrConflict)
	got, err := s.GetTradingAccountByID(context.Background(), "account-1")
	require.NoError(t, err)
	require.Equal(t, "BINANCE_STANDARD", got.AccountMode)
}
