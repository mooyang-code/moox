package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestPaperBalanceIndexMigrationDoesNotMutateUnknownFillSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown-fill.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.db.Exec("DROP INDEX idx_order_fills_paper_balance_history").Error)
	require.NoError(t, s.db.Exec("ALTER TABLE t_order_fills ADD COLUMN c_unrecognized TEXT").Error)
	require.NoError(t, s.Close())
	reopened, err := Open(path)
	if reopened != nil {
		defer reopened.Close()
	}
	require.ErrorIs(t, err, ErrIncompatibleSchema)
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := raw.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	var indexes int64
	require.NoError(t, raw.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_order_fills_paper_balance_history'").Scan(&indexes).Error)
	require.Zero(t, indexes)
}

func TestPaperBalanceMigrationRejectsMalformedStoredDecimalAtomically(t *testing.T) {
	for _, column := range []string{"c_price", "c_quantity", "c_fee", "c_realized_pnl"} {
		t.Run(column, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.db")
			s, err := Open(path)
			require.NoError(t, err)
			fill := seedPaperBalanceOrder(t, s)
			require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
			fill.FillID, fill.ExchangeTradeID = "z-bad-fill", "z-bad-trade"
			require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
			require.NoError(t, s.db.Exec(fmt.Sprintf("UPDATE t_order_fills SET %s = '' WHERE c_fill_id = 'z-bad-fill'", column)).Error)
			require.NoError(t, s.db.Exec("DROP TABLE t_paper_asset_balances").Error)
			require.NoError(t, s.db.Exec("DROP TABLE t_paper_balance_projections").Error)
			require.NoError(t, s.Close())
			opened, err := Open(path)
			if opened != nil {
				defer opened.Close()
			}
			require.ErrorIs(t, err, ErrInvalidRecord)
			raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := raw.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			for _, table := range []string{"t_paper_asset_balances", "t_paper_balance_projections"} {
				var count int64
				require.NoError(t, raw.Table(table).Count(&count).Error)
				require.Zero(t, count, "failed migration must roll back balances and initialization marker")
			}
		})
	}
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

func TestPaperBalanceHistoryRejectsAmbiguousFillAssetsAtomically(t *testing.T) {
	for _, name := range []string{"swap settlement mismatch", "missing fee asset"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ambiguous.db")
			s, err := Open(path)
			require.NoError(t, err)
			fill := seedPaperBalanceOrder(t, s)
			if name == "swap settlement mismatch" {
				require.NoError(t, s.db.Exec("UPDATE t_trading_accounts SET c_market_type = 'SWAP'").Error)
				require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_market_type = 'SWAP'").Error)
				fill.RealizedPnL = "10"
			}
			require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
			fill.FillID, fill.ExchangeTradeID = "z-bad-fill", "z-bad-trade"
			require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
			if name == "swap settlement mismatch" {
				require.NoError(t, s.db.Exec("UPDATE t_order_fills SET c_settlement_asset = 'BTC' WHERE c_fill_id = 'z-bad-fill'").Error)
			} else {
				require.NoError(t, s.db.Exec("UPDATE t_order_fills SET c_fee_asset = '' WHERE c_fill_id = 'z-bad-fill'").Error)
			}
			require.NoError(t, s.db.Exec("DROP TABLE t_paper_asset_balances").Error)
			require.NoError(t, s.db.Exec("DROP TABLE t_paper_balance_projections").Error)
			require.NoError(t, s.db.Exec("DROP INDEX idx_order_fills_paper_balance_history").Error)
			require.NoError(t, s.Close())
			reopened, err := Open(path)
			if reopened != nil {
				defer reopened.Close()
			}
			require.ErrorIs(t, err, ErrInvalidRecord)
			raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := raw.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			var count int64
			for _, table := range []string{"t_paper_balance_projections", "t_paper_asset_balances"} {
				require.NoError(t, raw.Table(table).Count(&count).Error)
				require.Zero(t, count, "valid replay prefix must also roll back")
			}
			require.NoError(t, raw.Table("t_order_fills").Count(&count).Error)
			require.Equal(t, int64(2), count, "migration must retain historical facts for diagnosis")
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

func TestPaperBalanceRoundTripIncrementalEqualsHistoryRebuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.db")
	s, err := Open(path)
	require.NoError(t, err)
	ctx := context.Background()
	spotBuy := seedPaperBalanceOrder(t, s)
	spotBuy.Fee, spotBuy.FeeAsset = "0.01", "BTC"
	swap := testAccount()
	swap.TradingAccountID, swap.Name, swap.MarketType, swap.ExecutionMode = "swap-account", "swap", "SWAP", "PAPER"
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(swap); err != nil {
			return err
		}
		if err := tx.UpsertInstrument(InstrumentRecord{Exchange: "BINANCE", MarketType: "SWAP", ExchangeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettlementAsset: "USDT", Linear: true, ContractValue: "1", ContractValueAsset: "BTC", ExchangeQuantityStep: "0.01", MinExchangeQuantity: "0.01", PriceTick: "0.01", Status: "TRADING"}); err != nil {
			return err
		}
		for _, order := range []OrderRecord{
			{SpaceID: spotBuy.SpaceID, TradingAccountID: spotBuy.TradingAccountID, OrderID: "spot-sell", ClientOrderID: "spot-sell", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "SELL", Quantity: "0.99", ReferencePrice: "110", OwnerType: "EXTERNAL", OwnerID: "spot-sell", State: "OPEN"},
			{SpaceID: swap.SpaceID, TradingAccountID: swap.TradingAccountID, OrderID: "swap-buy", ClientOrderID: "swap-buy", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "100", OwnerType: "EXTERNAL", OwnerID: "swap-buy", State: "OPEN"},
			{SpaceID: swap.SpaceID, TradingAccountID: swap.TradingAccountID, OrderID: "swap-sell", ClientOrderID: "swap-sell", ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "SELL", Quantity: "1", ReferencePrice: "112.3", OwnerType: "EXTERNAL", OwnerID: "swap-sell", State: "OPEN"},
		} {
			if err := tx.CreateOrder(order); err != nil {
				return err
			}
		}
		return nil
	}))
	fills := []FillRecord{
		spotBuy,
		{SpaceID: spotBuy.SpaceID, TradingAccountID: spotBuy.TradingAccountID, FillID: "spot-sell-fill", ExchangeTradeID: "spot-sell-trade", OrderID: "spot-sell", Price: "110", Quantity: "0.99", Fee: "0.1", FeeAsset: "USDT", SettlementAsset: "USDT", TradedAt: 124},
		{SpaceID: swap.SpaceID, TradingAccountID: swap.TradingAccountID, FillID: "swap-buy-fill", ExchangeTradeID: "swap-buy-trade", OrderID: "swap-buy", Price: "100", Quantity: "1", Fee: "0.1", FeeAsset: "USDT", SettlementAsset: "USDT", RealizedPnL: "0", TradedAt: 123},
		{SpaceID: swap.SpaceID, TradingAccountID: swap.TradingAccountID, FillID: "swap-sell-fill", ExchangeTradeID: "swap-sell-trade", OrderID: "swap-sell", Price: "112.3", Quantity: "1", Fee: "-0.02", FeeAsset: "BNB", SettlementAsset: "USDT", RealizedPnL: "12.3", TradedAt: 124},
	}
	for _, fill := range fills {
		require.NoError(t, s.Transaction(ctx, func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	}
	require.NoError(t, s.db.Exec("UPDATE t_trading_accounts SET c_status = 'CLOSED' WHERE c_trading_account_id = ?", spotBuy.TradingAccountID).Error)
	before := map[string]PaperBalanceSnapshot{}
	for _, accountID := range []string{spotBuy.TradingAccountID, swap.TradingAccountID} {
		before[accountID], err = s.GetPaperBalanceSnapshot(ctx, spotBuy.SpaceID, accountID)
		require.NoError(t, err)
	}
	require.Equal(t, "100008.8", before[spotBuy.TradingAccountID].Totals["USDT"].String())
	require.True(t, before[spotBuy.TradingAccountID].Totals["BTC"].IsZero(), "spot buy and sell must net base inventory to zero after base fee")
	require.Equal(t, "100012.2", before[swap.TradingAccountID].Totals["USDT"].String())
	require.Equal(t, "0.02", before[swap.TradingAccountID].Totals["BNB"].String())
	beforeFills, _, err := s.ListFills(ctx, spotBuy.SpaceID, FillQuery{Limit: 100})
	require.NoError(t, err)
	var beforeOrders []orderRow
	require.NoError(t, s.db.Table("t_trade_orders").Order("c_order_id").Find(&beforeOrders).Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_paper_asset_balances").Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_paper_balance_projections").Error)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	decimalStrings := func(values map[string]shared.Decimal) map[string]string {
		result := map[string]string{}
		for asset, value := range values {
			result[asset] = value.String()
		}
		return result
	}
	for accountID, expected := range before {
		actual, err := s.GetPaperBalanceSnapshot(ctx, spotBuy.SpaceID, accountID)
		require.NoError(t, err)
		require.Equal(t, expected.AppliedFillCount, actual.AppliedFillCount)
		require.Equal(t, decimalStrings(expected.Totals), decimalStrings(actual.Totals))
		require.Equal(t, decimalStrings(expected.Reserved), decimalStrings(actual.Reserved))
	}
	account, err := s.GetTradingAccount(ctx, spotBuy.SpaceID, spotBuy.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "CLOSED", account.Status)
	afterFills, _, err := s.ListFills(ctx, spotBuy.SpaceID, FillQuery{Limit: 100})
	require.NoError(t, err)
	require.Equal(t, beforeFills, afterFills)
	var afterOrders []orderRow
	require.NoError(t, s.db.Table("t_trade_orders").Order("c_order_id").Find(&afterOrders).Error)
	require.Equal(t, beforeOrders, afterOrders)
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

func TestPaperBalanceBackfillBeyond100001Fills(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := Open(path)
	require.NoError(t, err)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.db.Exec("UPDATE t_trade_orders SET c_quantity = '2000', c_filled_quantity = '1000.02', c_average_price = '0.1', c_state = 'PARTIALLY_FILLED'").Error)
	require.NoError(t, s.db.Exec(`WITH RECURSIVE fills(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM fills WHERE n < 100002)
 INSERT INTO t_order_fills (c_space_id, c_fill_id, c_exchange_trade_id, c_order_id, c_exchange_order_id, c_trading_account_id, c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_side, c_position_side, c_price, c_quantity, c_fee, c_fee_asset, c_settlement_asset, c_realized_pnl, c_role, c_traded_at)
 SELECT 'space-1', printf('fill-%06d', n), printf('trade-%06d', n), 'paper-order', '', 'account-1', 'BINANCE', 'SPOT', 'BTCUSDT', 'BTCUSDT', 'BUY', '', '0.1', '0.01', '0.00001', 'USDT', 'USDT', '0', '', n / 3000 FROM fills`).Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_paper_asset_balances").Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_paper_balance_projections").Error)
	require.NoError(t, s.db.Exec("DROP INDEX idx_order_fills_paper_balance_history").Error)
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, int64(100002), snapshot.AppliedFillCount)
	require.Equal(t, "99898.99798", snapshot.Totals["USDT"].String())
	require.Equal(t, "1000.02", snapshot.Totals["BTC"].String())
	// Old-timestamp arrivals must still apply after the full backfill.
	fill.TradedAt, fill.FillID, fill.ExchangeTradeID = -1, "late-fill", "late-trade"
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	require.NoError(t, s.Close())
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	snapshot, err = s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, int64(100003), snapshot.AppliedFillCount)
	require.Equal(t, "99798.89798", snapshot.Totals["USDT"].String())
}

func TestPaperBalanceOpenBackfillsOnceAndKeepsClosedAccount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paper.db")
	s, err := Open(path)
	require.NoError(t, err)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	require.NoError(t, s.db.Exec("UPDATE t_trading_accounts SET c_status = 'CLOSED'").Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_paper_asset_balances").Error)
	require.NoError(t, s.db.Exec("DROP TABLE t_paper_balance_projections").Error)
	require.NoError(t, s.db.Exec("DROP INDEX idx_order_fills_paper_balance_history").Error)
	require.NoError(t, s.Close())
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		require.NoError(t, err)
		snapshot, err := s.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
		require.NoError(t, err)
		require.Equal(t, "99899.9", snapshot.Totals["USDT"].String())
		require.Equal(t, int64(1), snapshot.AppliedFillCount)
		account, err := s.GetTradingAccount(context.Background(), fill.SpaceID, fill.TradingAccountID)
		require.NoError(t, err)
		require.Equal(t, "CLOSED", account.Status)
		if i == 0 {
			require.NoError(t, s.db.Exec("UPDATE t_paper_account_configs SET c_initial_balance = '200000'").Error)
		}
		require.NoError(t, s.Close())
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
