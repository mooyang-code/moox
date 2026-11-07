package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 引用号序号按这个前缀下全部记录取最大值加一，不受 24 小时窗口影响，不会和很早以前的同号记录撞上唯一约束。
func TestNextWalletTransferAttemptIgnoresTimeWindow(t *testing.T) {
	ctx := context.Background()
	tradeStore := seedTransferParents(t)
	const prefix = "margin-logical-1-20261101T000000:TO_FUTURES:"
	next := func() int {
		var value int
		require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
			var err error
			value, err = tx.NextWalletTransferAttempt("space-1", "account-1", prefix)
			return err
		}))
		return value
	}
	require.Equal(t, 1, next())
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
		// 一条几天前的记录（不在任何时间窗口里）和一条第 10 次尝试。
		if err := tx.InsertWalletTransfer(transferRecord("t-1", prefix+"1", TransferToFutures, 1)); err != nil {
			return err
		}
		if err := tx.InsertWalletTransfer(transferRecord("t-2", prefix+"10", TransferToFutures, 2)); err != nil {
			return err
		}
		// 别的前缀不算。
		return tx.InsertWalletTransfer(transferRecord("t-3", "target-1:TO_FUTURES:42", TransferToFutures, 3))
	}))
	require.Equal(t, 11, next())
}

// 更新划转记录时流水号传空串，保留已经记下的流水号。
func TestUpdateWalletTransferKeepsExchangeIDWhenEmpty(t *testing.T) {
	ctx := context.Background()
	tradeStore := seedTransferParents(t)
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *Tx) error {
		if err := tx.InsertWalletTransfer(transferRecord("t-1", "target-1:TO_SPOT:1", TransferToSpot, 1000)); err != nil {
			return err
		}
		if err := tx.UpdateWalletTransfer("space-1", "t-1", "tran-9", TransferStatePending, "", 0); err != nil {
			return err
		}
		return tx.UpdateWalletTransfer("space-1", "t-1", "", TransferStateFailed, "人工结束", 2000)
	}))
	rows, err := tradeStore.ListWalletTransfersForTarget(ctx, "space-1", "logical-1", "target-1", "account-1", TransferToSpot)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "tran-9", rows[0].ExchangeTransferID)
	require.Equal(t, TransferStateFailed, rows[0].State)
}
