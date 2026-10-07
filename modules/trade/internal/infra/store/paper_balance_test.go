package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaperBalanceInitializedWithAccount(t *testing.T) {
	s := openTestStore(t)
	a := testAccount()
	a.ExecutionMode = "PAPER"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(a) }))
	var count int64
	err := s.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 't_paper_balance_projections'").Scan(&count).Error
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "Paper accounts need durable initialized balance projections")
	require.NoError(t, s.db.Raw("SELECT COUNT(*) FROM t_paper_balance_projections").Scan(&count).Error)
	require.Equal(t, int64(1), count, "creation must initialize the balance in its transaction")
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), a.SpaceID, a.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "100000", snapshot.Totals[a.SettlementAsset].String())
}

func TestPaperBalanceInitializationTimestampPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timestamp.db")
	s, err := Open(path)
	require.NoError(t, err)
	seedPaperBalanceOrder(t, s)
	var columnCount int64
	require.NoError(t, s.db.Raw("SELECT COUNT(*) FROM pragma_table_info('t_paper_balance_projections') WHERE name = 'c_initialized_at'").Scan(&columnCount).Error)
	require.Equal(t, int64(1), columnCount, "initialization metadata must include its audit timestamp")
	var first int64
	require.NoError(t, s.db.Raw("SELECT c_initialized_at FROM t_paper_balance_projections").Scan(&first).Error)
	require.Positive(t, first)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	var reopened int64
	require.NoError(t, s.db.Raw("SELECT c_initialized_at FROM t_paper_balance_projections").Scan(&reopened).Error)
	require.Equal(t, first, reopened)
}

func TestPaperBalanceInitializationFailureRollsBackAccount(t *testing.T) {
	s := openTestStore(t)
	require.NoError(t, s.db.Exec(`CREATE TRIGGER fail_paper_initialization BEFORE INSERT ON t_paper_asset_balances BEGIN SELECT RAISE(ABORT, 'injected initialization failure'); END`).Error)
	a := testAccount()
	a.ExecutionMode = "PAPER"
	err := s.Transaction(context.Background(), func(tx *Tx) error { return tx.CreateTradingAccount(a) })
	require.Error(t, err)
	for _, table := range []string{"t_trading_accounts", "t_paper_account_configs", "t_paper_balance_projections", "t_paper_asset_balances"} {
		var count int64
		require.NoError(t, s.db.Table(table).Count(&count).Error)
		require.Zero(t, count)
	}
}

func TestPaperBalanceOpenRejectsUnknownProjectionShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shape.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("ALTER TABLE t_paper_asset_balances ADD COLUMN c_unrecognized TEXT").Error)
	require.NoError(t, s.Close())
	reopened, err := Open(path)
	if reopened != nil {
		defer reopened.Close()
	}
	require.ErrorIs(t, err, ErrIncompatibleSchema)
}

func TestPaperBalanceRollbackAndImmutableConflict(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	abort := errors.New("abort after fill")
	require.ErrorIs(t, s.Transaction(context.Background(), func(tx *Tx) error {
		_, err := tx.InsertFill(fill)
		if err != nil {
			return err
		}
		return abort
	}), abort)
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "100000", snapshot.Totals["USDT"].String())
	require.Zero(t, snapshot.AppliedFillCount)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	fill.Fee = "0.2"
	require.ErrorIs(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }), ErrConflict)
	snapshot, err = s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "99899.9", snapshot.Totals["USDT"].String())
	require.Equal(t, int64(1), snapshot.AppliedFillCount)
}

func TestPaperBalanceFillProjectionFailureRollsBackFact(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.db.Exec("UPDATE t_trade_instruments SET c_base_asset = ''").Error)
	require.ErrorIs(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }), ErrInvalidRecord)
	var count int64
	require.NoError(t, s.db.Table("t_order_fills").Count(&count).Error)
	require.Zero(t, count)
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Zero(t, snapshot.AppliedFillCount)
	require.Equal(t, "100000", snapshot.Totals["USDT"].String())
}

func TestPaperBalanceRejectsAmbiguousFillAssetsAtomically(t *testing.T) {
	for _, name := range []string{"swap settlement mismatch", "spot missing fee asset", "swap missing fee asset", "rebate missing fee asset"} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			fill := seedPaperBalanceOrder(t, s)
			if name == "swap settlement mismatch" || name == "swap missing fee asset" {
				require.NoError(t, s.db.Exec("UPDATE t_trading_accounts SET c_market_type = 'SWAP'").Error)
				require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_market_type = 'SWAP'").Error)
				fill.RealizedPnL = "10"
			}
			if name == "swap settlement mismatch" {
				fill.SettlementAsset = "BTC"
			} else {
				fill.FeeAsset = ""
			}
			if name == "rebate missing fee asset" {
				fill.Fee = "-0.1"
			}
			err := s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err })
			require.ErrorIs(t, err, ErrInvalidRecord)
			snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
			require.NoError(t, err)
			require.Equal(t, "100000", snapshot.Totals["USDT"].String())
			require.Len(t, snapshot.Totals, 1)
			require.Zero(t, snapshot.AppliedFillCount)
			var count int64
			require.NoError(t, s.db.Table("t_order_fills").Count(&count).Error)
			require.Zero(t, count, "invalid fill and its projection must roll back together")
		})
	}
}

func TestPaperBalanceSwapSettlementNormalizationRetainsActualFeeAsset(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.db.Exec("UPDATE t_trading_accounts SET c_market_type = 'SWAP'").Error)
	require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_market_type = 'SWAP'").Error)
	fill.SettlementAsset, fill.FeeAsset, fill.Fee, fill.RealizedPnL = "", "BNB", "-0.1", "10"
	for _, expectedInserted := range []bool{true, false} {
		require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
			inserted, err := tx.InsertFill(fill)
			require.Equal(t, expectedInserted, inserted)
			return err
		}))
	}
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "100010", snapshot.Totals["USDT"].String())
	require.Equal(t, "0.1", snapshot.Totals["BNB"].String())
	require.Equal(t, int64(1), snapshot.AppliedFillCount)
}

func TestPaperBalanceSellAndSwapCashFlows(t *testing.T) {
	for _, market := range []string{"SPOT", "SWAP"} {
		t.Run(market, func(t *testing.T) {
			s := openTestStore(t)
			fill := seedPaperBalanceOrder(t, s)
			if market == "SWAP" {
				require.NoError(t, s.db.Exec("UPDATE t_trading_accounts SET c_market_type = 'SWAP'").Error)
				require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_market_type = 'SWAP', c_side = 'SELL'").Error)
			} else {
				require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_side = 'SELL'").Error)
			}
			fill.RealizedPnL = "12.3"
			require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
			snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
			require.NoError(t, err)
			if market == "SWAP" {
				require.Equal(t, "100012.2", snapshot.Totals["USDT"].String())
				require.Len(t, snapshot.Totals, 1, "swap notionals must not be booked as spot assets")
			} else {
				require.Equal(t, "100099.9", snapshot.Totals["USDT"].String())
				require.Equal(t, "-1", snapshot.Totals["BTC"].String())
			}
		})
	}
}

func TestPaperBalanceFillCrossOrderAndLateFeeConflict(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	fill.Fee = "0"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	fill.Fee = "0.1"
	require.ErrorIs(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }), ErrConflict)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		return tx.CreateOrder(OrderRecord{SpaceID: fill.SpaceID, TradingAccountID: fill.TradingAccountID, OrderID: "other-order", ClientOrderID: "other-client", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "other-client", State: "OPEN"})
	}))
	fill.Fee, fill.FillID, fill.OrderID = "0", "other-fill", "other-order"
	require.ErrorIs(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }), ErrConflict)
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "99900", snapshot.Totals["USDT"].String())
	require.Equal(t, int64(1), snapshot.AppliedFillCount)
}

func TestPaperBalanceFeeAssetsRebatesAndLateTrade(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	for i, item := range []struct{ fee, asset string }{{"0.1", "USDT"}, {"0.01", "BTC"}, {"0.02", "BNB"}, {"-0.003", "BNB"}} {
		fill.FillID, fill.ExchangeTradeID = fmt.Sprint("fill-", i), fmt.Sprint("trade-", i)
		fill.TradedAt = int64(100 - i)
		fill.Fee, fill.FeeAsset = item.fee, item.asset
		require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	}
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "99599.9", snapshot.Totals["USDT"].String())
	require.Equal(t, "3.99", snapshot.Totals["BTC"].String())
	require.Equal(t, "-0.017", snapshot.Totals["BNB"].String())
	require.Equal(t, int64(4), snapshot.AppliedFillCount)
}

func TestPaperBalanceReservationsOnlyActiveOrders(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	for _, state := range []string{"PENDING", "SUBMITTING", "SUBMIT_UNKNOWN", "OPEN", "PARTIALLY_FILLED", "CANCELING", "CANCEL_UNKNOWN", "FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED"} {
		require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_state = ?, c_reserved_asset = 'USDT', c_reserved_quantity = '0.123456789012345678', c_remaining_reserved_quantity = '0.123456789012345678'", state).Error)
		snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
		require.NoError(t, err)
		switch state {
		case "FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED":
			require.Empty(t, snapshot.Reserved)
		default:
			require.Equal(t, "0.123456789012345678", snapshot.Reserved["USDT"].String())
		}
	}
}

func seedPaperBalanceOrder(t *testing.T, s *Store) FillRecord {
	t.Helper()
	ctx := context.Background()
	a := testAccount()
	a.ExecutionMode = "PAPER"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(a); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01", ExchangeQuantityStep: "0.0001", Status: "TRADING"}); err != nil {
			return err
		}
		return tx.CreateOrder(OrderRecord{SpaceID: a.SpaceID, TradingAccountID: a.TradingAccountID, OrderID: "paper-order", ClientOrderID: "paper-client", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "10", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "paper-client", State: "OPEN"})
	}))
	return FillRecord{SpaceID: a.SpaceID, TradingAccountID: a.TradingAccountID, FillID: "paper-fill", ExchangeTradeID: "paper-trade", OrderID: "paper-order", Price: "100", Quantity: "1", Fee: "0.1", FeeAsset: "USDT", SettlementAsset: "USDT", TradedAt: 123}
}

func TestPaperBalanceNewFillAndReplay(t *testing.T) {
	s := openTestStore(t)
	fill := seedPaperBalanceOrder(t, s)
	for _, wantInserted := range []bool{true, false} {
		require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
			inserted, err := tx.InsertFill(fill)
			require.Equal(t, wantInserted, inserted)
			return err
		}))
		snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
		require.NoError(t, err)
		require.Equal(t, "99899.9", snapshot.Totals["USDT"].String())
		require.Equal(t, "1", snapshot.Totals["BTC"].String())
		require.Equal(t, int64(1), snapshot.AppliedFillCount)
	}
}
