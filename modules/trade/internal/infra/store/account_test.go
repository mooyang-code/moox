package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateExchangeAccountValidatesAndCanonicalizesTypedJSON(t *testing.T) {
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
		return tx.CreateExchangeAccount(account)
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

func TestCreateExchangeAccountRejectsMalformedTypedJSONValues(t *testing.T) {
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
				return tx.CreateExchangeAccount(account)
			})
			require.ErrorIs(t, err, ErrInvalidRecord)
		})
	}
}

func TestExchangeAccountScopedUpdatesDoNotOverwriteOtherResponsibilities(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	account := testAccount()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(account)
	}))

	staleConfig := ExchangeAccountConfiguration{
		Name: "renamed", CredentialSecretID: "secret-2",
		SettlementAsset: "USDC", MarginMode: "CROSS", Status: "ENABLED",
		SyncSymbols: []string{"BTCUSDT"},
	}
	syncState := ExchangeAccountSyncState{
		Ready: true, LeverageSettings: LeverageSettings{"BTCUSDT": "5"},
		Snapshot: ExchangeAccountSnapshot{
			Equity: "100", AvailableFunds: "80", UsedMargin: "20",
			MaintenanceMargin: "2", UnrealizedPnL: "-1",
			ExchangeUpdatedAt: 1000,
		},
		SnapshotSourceTime: 1000, LastSyncAt: 1001, LastReadyAt: 1001,
	}
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateExchangeAccountSync("space-1", "account-1", syncState)
	}))
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateExchangeAccountConfiguration("space-1", "account-1", staleConfig)
	}))
	syncState.Ready = false
	syncState.LastSyncAt = 1002
	syncState.LastError = "disconnected"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateExchangeAccountSync("space-1", "account-1", syncState)
	}))

	got, err := s.GetExchangeAccount(ctx, "space-1", "account-1")
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.Equal(t, "secret-2", got.CredentialSecretID)
	require.Equal(t, "USDC", got.SettlementAsset)
	require.Equal(t, []string{"BTCUSDT"}, got.SyncSymbols)
	require.False(t, got.Ready)
	require.Equal(t, "5", got.LeverageSettings["BTCUSDT"])
	require.Equal(t, "100", got.Snapshot.Equity)
	require.Equal(t, int64(1002), got.LastSyncAt)
	require.Equal(t, "disconnected", got.LastError)
}

func TestCreateExchangeAccountIsExplicitAndRejectsDuplicate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	account := testAccount()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(account)
	}))
	err := s.Transaction(ctx, func(tx *Tx) error {
		account.Status = "DISABLED"
		return tx.CreateExchangeAccount(account)
	})
	require.ErrorIs(t, err, ErrConflict)

	got, err := s.GetExchangeAccount(ctx, account.SpaceID, account.ExchangeAccountID)
	require.NoError(t, err)
	require.Equal(t, "ENABLED", got.Status)
}

func TestExchangeAccountCredentialIsRequiredOnlyForLive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	paper := testAccount()
	paper.CredentialSecretID = ""
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(paper)
	}))

	live := testAccount()
	live.ExchangeAccountID = "account-live"
	live.Name = "live"
	live.ExecutionMode = "LIVE"
	live.Environment = "TESTNET"
	live.CredentialSecretID = ""
	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateExchangeAccount(live)
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
}

func TestExchangeAccountSettlementAssetCannotChangeWhileMember(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateExchangeAccount(testAccount()); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			ExecutionMode: "PAPER", MarketType: "SPOT", SettlementAsset: "USDT",
			AutomationState: "PAUSED", PauseReason: "configure",
		}); err != nil {
			return err
		}
		return tx.PutLogicalAccountMember(LogicalAccountMemberRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1",
			ExchangeAccountID: "account-1", Enabled: true,
		})
	}))

	err := s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateExchangeAccountConfiguration(
			"space-1",
			"account-1",
			ExchangeAccountConfiguration{
				Name: "main", CredentialSecretID: "secret-1",
				SettlementAsset: "USDC", Status: "ENABLED",
			},
		)
	})

	require.ErrorIs(t, err, ErrConflict)
}

func testAccount() ExchangeAccountRecord {
	return ExchangeAccountRecord{
		SpaceID: "space-1", ExchangeAccountID: "account-1", Name: "main",
		Exchange: "BINANCE", MarketType: "SPOT", ExecutionMode: "PAPER",
		Environment:        "PAPER",
		CredentialSecretID: "secret-1", SettlementAsset: "USDT",
		Status: "ENABLED",
	}
}
