package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
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
			Exchange: "BINANCE", MarketType: "SPOT", Symbol: "BTCUSDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", PriceTick: "0.01",
			ExchangeQuantityStep: "0.0001", Status: "TRADING",
		}))
		return stop
	})
	require.ErrorIs(t, err, stop)

	var count int64
	require.NoError(t, s.db.Table("t_exchange_instruments").Count(&count).Error)
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

func TestOpenRejectsExchangeAccountWithoutCurrentCursorColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-account.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE t_exchange_accounts (
			c_space_id TEXT NOT NULL,
			c_exchange_account_id TEXT NOT NULL,
			c_name TEXT NOT NULL
		)
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
