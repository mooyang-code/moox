package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// 文件库的只读视图是单独的只读连接池：看得到读写连接已提交的数据，拒绝写入，读写连接上开着没提交的事务时照样能读；
// 和读写连接共用标的缓存。内存库没有单独的只读连接，只读视图就是自己。
func TestReadOnlyView(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ro := s.ReadOnly()
	require.NotSame(t, s, ro)
	require.Same(t, ro, ro.ReadOnly())
	var queryOnly int
	require.NoError(t, ro.db.Raw(`PRAGMA query_only`).Scan(&queryOnly).Error)
	require.Equal(t, 1, queryOnly)
	seedLogicalAccount(t, s, "")
	record, err := ro.GetLogicalAccount(ctx, "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "logical", record.Name, "只读视图看得到已提交的数据")

	err = ro.Transaction(ctx, func(tx *Tx) error {
		return tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-2", Name: "other", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "测试",
		})
	})
	require.ErrorContains(t, err, "readonly", "经只读视图写库会被拒绝")

	// 读写连接上开着没提交的事务时，经只读视图的查询照样在短超时内返回，读到的是已提交的数据；
	// 同样的查询走读写连接要排在这个事务后面，等到超时。
	require.NoError(t, s.db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`UPDATE t_logical_accounts SET c_name = 'pending'`).Error)
		blocked, cancelBlocked := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancelBlocked()
		_, err := s.GetLogicalAccount(blocked, "space-1", "logical-1")
		require.ErrorIs(t, err, context.DeadlineExceeded)

		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		started := time.Now()
		record, err := ro.GetLogicalAccount(short, "space-1", "logical-1")
		require.NoError(t, err)
		require.Equal(t, "logical", record.Name, "看不到没提交的修改")
		_, err = ro.ListLogicalAccountMembers(short, "space-1", "logical-1", true)
		require.NoError(t, err)
		_, err = ro.ListTradingAccounts(short, "space-1")
		require.NoError(t, err)
		_, err = ro.ListPositionsByInstrument(short, "space-1", "account-1", "")
		require.NoError(t, err)
		_, _, err = ro.ListFills(short, "space-1", FillQuery{Limit: 10})
		require.NoError(t, err)
		require.Less(t, time.Since(started), time.Second)
		return nil
	}))

	// 只读视图先读过一次标的（缓存里是空的），读写连接写入标的后，只读视图读到新的标的
	instruments, err := ro.ListInstruments(ctx, "BINANCE", "SPOT")
	require.NoError(t, err)
	require.Empty(t, instruments)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		return tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			ExchangeQuantityStep: "0.0001", Status: "TRADING",
		})
	}))
	instruments, err = ro.ListInstruments(ctx, "BINANCE", "SPOT")
	require.NoError(t, err)
	require.Len(t, instruments, 1)

	memory, err := Open(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, memory.Close()) }()
	require.Same(t, memory, memory.ReadOnly())
}

func TestOpenConfiguresSQLiteAndTransactionRollback(t *testing.T) {
	s := openTestStore(t)

	var foreignKeys, busyTimeout int
	require.NoError(t, s.db.Raw(`PRAGMA foreign_keys`).Scan(&foreignKeys).Error)
	require.Equal(t, 1, foreignKeys)
	require.NoError(t, s.db.Raw(`PRAGMA busy_timeout`).Scan(&busyTimeout).Error)
	require.Equal(t, sqliteBusyTimeoutMS, busyTimeout)

	stop := errors.New("stop")
	err := s.Transaction(context.Background(), func(tx *Tx) error {
		require.NoError(t, tx.UpsertInstrument(InstrumentRecord{
			Exchange: "BINANCE", MarketType: "SPOT", ExchangeSymbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			ExchangeQuantityStep: "0.0001", Status: "TRADING",
		}))
		return stop
	})
	require.ErrorIs(t, err, stop)

	var count int64
	require.NoError(t, s.db.Table("t_trade_instruments").Count(&count).Error)
	require.Zero(t, count)
}

func TestOpenRejectsObsoleteTradeSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE t_trade_channels (c_channel_id TEXT PRIMARY KEY)
	`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = Open(path)
	require.ErrorIs(t, err, ErrIncompatibleSchema)

	check, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, check.Raw(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	`).Scan(&count).Error)
	require.Equal(t, int64(1), count)
	checkSQL, err := check.DB()
	require.NoError(t, err)
	require.NoError(t, checkSQL.Close())
}

func TestOpenRejectsIncompatibleTargetExecutionColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incompatible.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE t_target_executions (
			c_space_id TEXT NOT NULL,
			c_execution_id TEXT NOT NULL,
			c_targets_json TEXT NOT NULL
		)
	`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = Open(path)
	require.ErrorIs(t, err, ErrIncompatibleSchema)
}

func TestOpenRejectsSingleAccountTargetAndLedgerSchema(t *testing.T) {
	for _, schemaSQL := range []string{
		`CREATE TABLE t_target_executions (
			c_space_id TEXT NOT NULL,
			c_execution_binding_id TEXT NOT NULL,
			c_trading_account_id TEXT NOT NULL
		)`,
		`CREATE TABLE t_ledger_transactions (
			c_space_id TEXT NOT NULL,
			c_transaction_id TEXT NOT NULL
		)`,
	} {
		t.Run(schemaSQL[:20], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "retired.db")
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			require.NoError(t, err)
			require.NoError(t, db.Exec(schemaSQL).Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			_, err = Open(path)
			require.ErrorIs(t, err, ErrIncompatibleSchema)
		})
	}
}

func TestOpenRejectsTradingAccountWithoutCurrentCursorColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-account.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE t_trading_accounts (
			c_space_id TEXT NOT NULL,
			c_trading_account_id TEXT NOT NULL,
			c_name TEXT NOT NULL
		)
	`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = Open(path)
	require.ErrorIs(t, err, ErrIncompatibleSchema)
}

func TestOpenRejectsCurrentTableWithoutRequiredUniqueConstraints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-fill.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE t_order_fills (
			c_space_id TEXT NOT NULL
		)
	`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = Open(path)

	require.ErrorIs(t, err, ErrIncompatibleSchema)
}

func TestOpenRejectsChangedPartialUniqueIndexPredicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-index.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AllSQL()).Error)
	require.NoError(t, db.Exec(`DROP INDEX uk_trade_orders_exchange_order`).Error)
	require.NoError(t, db.Exec(`
		CREATE UNIQUE INDEX uk_trade_orders_exchange_order
		ON t_trade_orders (
			c_space_id, c_trading_account_id, c_exchange_symbol, c_exchange_order_id
		)
		WHERE c_exchange_order_id = ''
	`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = Open(path)
	require.ErrorIs(t, err, ErrIncompatibleSchema)
}

func TestOpenAcceptsCurrentSchemaOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.db")
	first, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, second.Close())
}

func TestListActiveOrdersForAccountSkipsFinishedOrders(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	require.NoError(t, s.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(TradingAccountRecord{SpaceID: "s", TradingAccountID: "a", Name: "a", Exchange: "BINANCE", Markets: []string{"SPOT"}, ExecutionMode: "PAPER", SettlementAsset: "USDT", Status: "ENABLED"}); err != nil {
			return err
		}
		for id, state := range map[string]string{"open": "OPEN", "unknown": "SUBMIT_UNKNOWN", "filled": "FILLED", "canceled": "CANCELED"} {
			if err := tx.CreateOrder(OrderRecord{SpaceID: "s", OrderID: id, TradingAccountID: "a", ClientOrderID: id, ExchangeSymbol: "BTCUSDT", OrderType: "MARKET", Side: "BUY", Quantity: "1", ReferencePrice: "1", ReferencePriceAt: 1, OwnerType: "EXTERNAL", OwnerID: id, State: state}); err != nil {
				return err
			}
		}
		return nil
	}))
	records, err := s.ListActiveOrdersForAccount(ctx, "s", "a")
	require.NoError(t, err)
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.OrderID)
	}
	require.ElementsMatch(t, []string{"open", "unknown"}, ids)
}
