package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedTransferParents(t *testing.T) *Store {
	t.Helper()
	tradeStore, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	require.NoError(t, tradeStore.Transaction(context.Background(), func(tx *Tx) error {
		if err := tx.CreateTradingAccount(TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "main", Exchange: "BINANCE", Markets: []string{"SPOT", "SWAP"},
			ExecutionMode: "LIVE", AccountMode: "BINANCE_STANDARD", Environment: "PRODUCTION", CredentialSecretID: "secret-1",
			SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED", LeverageSettings: LeverageSettings{DefaultLeverageKey: "1"},
		}); err != nil {
			return err
		}
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "l", OwnerInstanceID: "r", OwnerSessionID: "s",
			ExecutionMode: "LIVE", SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "x",
		})
	}))
	return tradeStore
}

func transferRecord(id, ref, direction string, requestedAt int64) WalletTransferRecord {
	return WalletTransferRecord{
		SpaceID: "space-1", TransferID: id, TradingAccountID: "account-1", LogicalAccountID: "logical-1", TargetID: "target-1",
		Direction: direction, Asset: "USDT", Amount: "100", ClientRef: ref, RequestedAt: requestedAt,
	}
}

func TestWalletTransferLifecycle(t *testing.T) {
	ctx := context.Background()
	tradeStore := seedTransferParents(t)
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertWalletTransfer(transferRecord("t-1", "target-1:TO_SPOT:1", TransferToSpot, 1000))
	}))
	// 本地引用号重复被拒绝。
	err := tradeStore.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertWalletTransfer(transferRecord("t-2", "target-1:TO_SPOT:1", TransferToSpot, 2000))
	})
	require.ErrorIs(t, err, ErrConflict)

	rows, err := tradeStore.ListWalletTransfersForTarget(ctx, "space-1", "logical-1", "target-1", "account-1", TransferToSpot)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, TransferStatePending, rows[0].State, "默认 PENDING")

	// 写入交易所流水号后仍是 PENDING，再完成。
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateWalletTransfer("space-1", "t-1", "tran-9", TransferStatePending, "", 0)
	}))
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateWalletTransfer("space-1", "t-1", "tran-9", TransferStateCompleted, "", 3000)
	}))
	// 终态不再改动。
	err = tradeStore.Transaction(ctx, func(tx *Tx) error {
		return tx.UpdateWalletTransfer("space-1", "t-1", "tran-9", TransferStateFailed, "x", 0)
	})
	require.ErrorIs(t, err, ErrNotPending)
	rows, err = tradeStore.ListWalletTransfersForTarget(ctx, "space-1", "logical-1", "target-1", "account-1", TransferToSpot)
	require.NoError(t, err)
	require.Equal(t, "tran-9", rows[0].ExchangeTransferID)
	require.Equal(t, TransferStateCompleted, rows[0].State)
	require.EqualValues(t, 3000, rows[0].CompletedAt)

	// 按编号读取；封顶标记随记录保存。
	capped := transferRecord("t-cap", "target-1:TO_SPOT:2", TransferToSpot, 4000)
	capped.Capped = true
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error { return tx.InsertWalletTransfer(capped) }))
	got, err := tradeStore.GetWalletTransfer(ctx, "space-1", "t-cap")
	require.NoError(t, err)
	require.True(t, got.Capped)
	plain, err := tradeStore.GetWalletTransfer(ctx, "space-1", "t-1")
	require.NoError(t, err)
	require.False(t, plain.Capped)
	_, err = tradeStore.GetWalletTransfer(ctx, "space-1", "missing")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// 另一个方向和另一个账户互不影响；按账户查按请求时间倒序。
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
		return tx.InsertWalletTransfer(transferRecord("t-3", "target-1:TO_FUTURES:1", TransferToFutures, 5000))
	}))
	back, err := tradeStore.ListWalletTransfersForTarget(ctx, "space-1", "logical-1", "target-1", "account-1", TransferToFutures)
	require.NoError(t, err)
	require.Len(t, back, 1)
	byAccount, err := tradeStore.ListWalletTransfersByAccount(ctx, "space-1", "account-1", 0, 10_000)
	require.NoError(t, err)
	require.Len(t, byAccount, 3, "t-3、封顶的 t-cap、t-1")
	require.Equal(t, "t-3", byAccount[0].TransferID)
	window, err := tradeStore.ListWalletTransfersByAccount(ctx, "space-1", "account-1", 0, 2000)
	require.NoError(t, err)
	require.Len(t, window, 1)
}

func TestWalletTransferRejectsIncompleteRecords(t *testing.T) {
	ctx := context.Background()
	tradeStore := seedTransferParents(t)
	for name, mutate := range map[string]func(*WalletTransferRecord){
		"方向不合法":  func(r *WalletTransferRecord) { r.Direction = "SIDEWAYS" },
		"没有金额":   func(r *WalletTransferRecord) { r.Amount = "" },
		"没有引用号":  func(r *WalletTransferRecord) { r.ClientRef = "" },
		"没有请求时间": func(r *WalletTransferRecord) { r.RequestedAt = 0 },
	} {
		record := transferRecord("t-x", "ref-x", TransferToSpot, 1000)
		mutate(&record)
		err := tradeStore.Transaction(ctx, func(tx *Tx) error { return tx.InsertWalletTransfer(record) })
		require.ErrorIs(t, err, ErrInvalidRecord, name)
	}
}
