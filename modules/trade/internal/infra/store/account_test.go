package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpsertExchangeAccountValidatesAndCanonicalizesTypedJSON(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	account := testAccount()
	account.LeverageSettings = LeverageSettings{
		"ETHUSDT": "5",
		"BTCUSDT": "10.0",
	}
	account.Snapshot = ExchangeAccountSnapshot{
		Balances: []AssetBalance{
			{Asset: "USDT", Available: "80", Locked: "20", Total: "100"},
		},
		Equity:            "100",
		AvailableFunds:    "80",
		UsedMargin:        "20",
		MaintenanceMargin: "2",
		UnrealizedPnL:     "-1.5",
		ExchangeUpdatedAt: 123,
	}

	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertExchangeAccount(account)
	}))

	var row struct {
		Leverage string `gorm:"column:c_leverage_settings_json"`
		Snapshot string `gorm:"column:c_snapshot_json"`
	}
	require.NoError(t, s.db.Table("t_exchange_accounts").
		Select("c_leverage_settings_json, c_snapshot_json").
		Where("c_space_id = ? AND c_exchange_account_id = ?", account.SpaceID, account.ExchangeAccountID).
		Take(&row).Error)
	require.Equal(t, `{"BTCUSDT":"10","ETHUSDT":"5"}`, row.Leverage)
	require.Equal(t,
		`{"balances":[{"asset":"USDT","available":"80","locked":"20","total":"100"}],`+
			`"equity":"100","available_funds":"80","used_margin":"20",`+
			`"maintenance_margin":"2","unrealized_pnl":"-1.5","exchange_updated_at":123}`,
		row.Snapshot)

	got, err := s.GetExchangeAccount(ctx, account.SpaceID, account.ExchangeAccountID)
	require.NoError(t, err)
	require.Equal(t, "10", got.LeverageSettings["BTCUSDT"])
	require.Equal(t, "-1.5", got.Snapshot.UnrealizedPnL)
}

func TestUpsertExchangeAccountRejectsMalformedTypedJSONValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExchangeAccountRecord)
	}{
		{
			name: "leverage",
			mutate: func(account *ExchangeAccountRecord) {
				account.LeverageSettings = LeverageSettings{"BTCUSDT": "many"}
			},
		},
		{
			name: "snapshot balance",
			mutate: func(account *ExchangeAccountRecord) {
				account.Snapshot.Balances = []AssetBalance{{
					Asset: "USDT", Available: "not-a-number", Locked: "0", Total: "0",
				}}
			},
		},
		{
			name: "snapshot margin",
			mutate: func(account *ExchangeAccountRecord) {
				account.Snapshot.UsedMargin = "{}"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			account := testAccount()
			tt.mutate(&account)

			err := s.Transaction(context.Background(), func(tx *Tx) error {
				return tx.UpsertExchangeAccount(account)
			})
			require.ErrorIs(t, err, ErrInvalidRecord)
		})
	}
}

func testAccount() ExchangeAccountRecord {
	return ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
		Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		Status: "ENABLED",
	}
}
