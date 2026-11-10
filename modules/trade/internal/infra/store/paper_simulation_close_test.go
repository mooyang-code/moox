package store

import (
	"context"
	"testing"
	"time"

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
	require.Equal(t, []AssetBalance{{Wallet: "UNIFIED", Asset: "USDT", Total: "100000", Available: "100000", Locked: "0", Borrowed: "0"}}, account.Snapshot.Balances)
}

// 模拟撤单与关闭账户时写的结束时间是毫秒时间戳，和订单上其它时间列一致。
func TestPaperCancelWritesMillisecondFinishedAt(t *testing.T) {
	for _, name := range []string{"cancel", "close"} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			fill := seedPaperBalanceOrder(t, s)
			ctx := context.Background()
			before := time.Now().UTC().UnixMilli()
			require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
				if name == "close" {
					// 非 OPEN 的在途订单走关闭账户的直接撤销路径。
					require.NoError(t, tx.db.Exec(`UPDATE t_trade_orders SET c_state = 'SUBMITTING'`).Error)
					return tx.ClosePaperSimulation(fill.SpaceID, fill.TradingAccountID)
				}
				order, err := tx.GetOrder(fill.SpaceID, fill.OrderID)
				if err != nil {
					return err
				}
				return tx.CancelPaperOrder(order, order.Version, "测试撤单")
			}))
			after := time.Now().UTC().UnixMilli()
			order, err := s.GetOrder(ctx, fill.SpaceID, fill.OrderID)
			require.NoError(t, err)
			require.Equal(t, "CANCELED", order.State)
			require.GreaterOrEqual(t, order.FinishedAt, before)
			require.LessOrEqual(t, order.FinishedAt, after)
		})
	}
}
