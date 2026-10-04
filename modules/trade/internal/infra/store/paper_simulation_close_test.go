package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClosePaperSimulationSnapshotAndReservationsRollbackTogether(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	ctx := context.Background()
	require.NoError(t, s.db.Exec(`UPDATE t_trade_orders SET c_reserved_asset='USDT', c_remaining_reserved_quantity='100'`).Error)
	before, err := s.GetTradingAccount(ctx, fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.NoError(t, s.db.Exec(`CREATE TRIGGER reject_paper_disable BEFORE UPDATE OF c_status ON t_trading_accounts
		WHEN NEW.c_status='DISABLED' BEGIN SELECT RAISE(ABORT, 'close failed'); END`).Error)
	err = s.Transaction(ctx, func(tx *Tx) error { return tx.ClosePaperSimulation(fill.SpaceID, fill.TradingAccountID) })
	require.ErrorContains(t, err, "close failed")
	after, err := s.GetTradingAccount(ctx, fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, before.Snapshot, after.Snapshot)
	require.Equal(t, before.Status, after.Status)
	order, err := s.GetOrder(ctx, fill.SpaceID, fill.OrderID)
	require.NoError(t, err)
	require.Equal(t, "OPEN", order.State)
	require.Equal(t, "100", order.RemainingReservedQuantity)
}

func TestClosePaperSimulationDoesNotInventValuationTimestamp(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	ctx := context.Background()
	require.NoError(t, s.db.Exec(`UPDATE t_trading_accounts SET c_snapshot_json='{}'`).Error)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { return tx.ClosePaperSimulation(fill.SpaceID, fill.TradingAccountID) }))
	account, err := s.GetTradingAccount(ctx, fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Zero(t, account.Snapshot.ExchangeUpdatedAt)
	require.Empty(t, account.Snapshot.Equity)
	require.Equal(t, "100000", account.Snapshot.AvailableFunds)
	require.Equal(t, []AssetBalance{{Asset: "USDT", Total: "100000", Available: "100000", Locked: "0"}}, account.Snapshot.Balances)
}
