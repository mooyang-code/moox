package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

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

func TestOpenMigratesLegacyLogicalAccountOwnerGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-owner.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(legacyLogicalAccountTableSQL).Error)
	require.NoError(t, db.Exec(`
CREATE UNIQUE INDEX ux_logical_account_owner_runner
ON t_logical_accounts (c_space_id, c_owner_runner_id)
WHERE c_owner_runner_id IS NOT NULL
`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO t_logical_accounts
 (c_space_id, c_logical_account_id, c_name, c_execution_mode, c_market_type, c_settlement_asset, c_automation_state, c_pause_reason)
VALUES ('space', 'logical', 'legacy', 'PAPER', 'SPOT', 'USDT', 'PAUSED', 'legacy')
;
INSERT INTO t_logical_accounts
 (c_space_id, c_logical_account_id, c_name, c_owner_runner_id, c_execution_mode, c_market_type, c_settlement_asset, c_automation_state, c_pause_reason)
VALUES ('space', 'owned', 'owned', 'runner', 'PAPER', 'SPOT', 'USDT', 'PAUSED', 'legacy')
`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	s, err := Open(path)
	require.NoError(t, err)
	var columns []string
	require.NoError(t, s.db.Raw(`SELECT name FROM pragma_table_info('t_logical_accounts') ORDER BY cid`).Scan(&columns).Error)
	require.Contains(t, columns, "c_owner_claimed_at")
	// The migration is additive and defaults existing accounts to generation 0.
	var generations map[string]int64
	var rows []struct {
		ID         string `gorm:"column:id"`
		Generation int64  `gorm:"column:generation"`
	}
	require.NoError(t, s.db.Raw(`SELECT c_logical_account_id AS id, c_owner_claimed_at AS generation FROM t_logical_accounts`).Scan(&rows).Error)
	generations = make(map[string]int64, len(rows))
	for _, row := range rows {
		generations[row.ID] = row.Generation
	}
	require.Equal(t, int64(0), generations["logical"])
	require.Equal(t, int64(1), generations["owned"])
	require.NoError(t, s.Close())
	reopened, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
}

func TestOpenMigratesLegacyLogicalAccountWithoutOwnerIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-no-owner-index.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(legacyLogicalAccountTableSQL).Error)
	require.NoError(t, db.Exec(`
INSERT INTO t_logical_accounts
 (c_space_id, c_logical_account_id, c_name, c_owner_runner_id, c_execution_mode, c_market_type, c_settlement_asset, c_automation_state, c_pause_reason)
VALUES ('space', 'owned', 'owned', 'runner', 'PAPER', 'SPOT', 'USDT', 'PAUSED', 'legacy')`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	check, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	var indexes []struct {
		Name string `gorm:"column:name"`
	}
	require.NoError(t, check.Raw(`PRAGMA index_list("t_logical_accounts")`).Scan(&indexes).Error)
	var ownerIndexFound bool
	for _, index := range indexes {
		if index.Name == "ux_logical_account_owner_runner" {
			ownerIndexFound = true
		}
	}
	require.True(t, ownerIndexFound)
	sqlDB, err = check.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

func TestOpenMigratesLegacyStrategyTargetTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-targets.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(legacyLogicalAccountTableSQL).Error)
	require.NoError(t, db.Exec(`
CREATE UNIQUE INDEX ux_logical_account_owner_runner
ON t_logical_accounts (c_space_id, c_owner_runner_id)
WHERE c_owner_runner_id IS NOT NULL`).Error)
	require.NoError(t, db.Exec(`
CREATE TABLE t_logical_account_targets (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_targets_json TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_blocked_targets_json TEXT NOT NULL DEFAULT '[]',
    c_last_error TEXT NOT NULL DEFAULT '',
    c_accepted_at INTEGER NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id),
    UNIQUE (c_space_id, c_target_id),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_command_sequence > 0),
    CHECK (c_status IN ('PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED')),
    CHECK (json_valid(c_targets_json)),
    CHECK (json_type(c_targets_json) = 'array'),
    CHECK (json_valid(c_blocked_targets_json)),
    CHECK (json_type(c_blocked_targets_json) = 'array')
)`).Error)
	require.NoError(t, db.Exec(`
CREATE TABLE t_logical_account_target_receipts (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_request_hash TEXT NOT NULL,
    c_signal_time INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_equity_source_time INTEGER NOT NULL,
    c_reference_prices_json TEXT NOT NULL,
    c_quantity_targets_json TEXT NOT NULL,
    c_accepted_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_target_id),
    UNIQUE (c_space_id, c_logical_account_id, c_runner_id, c_command_sequence),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_command_sequence > 0),
    CHECK (json_valid(c_weights_json)),
    CHECK (json_type(c_weights_json) = 'array'),
    CHECK (json_valid(c_reference_prices_json)),
    CHECK (json_type(c_reference_prices_json) = 'object'),
    CHECK (json_valid(c_quantity_targets_json)),
    CHECK (json_type(c_quantity_targets_json) = 'array')
)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO t_logical_accounts
 (c_space_id, c_logical_account_id, c_name, c_execution_mode, c_market_type, c_settlement_asset, c_automation_state, c_pause_reason)
VALUES ('space', 'logical', 'legacy', 'PAPER', 'SPOT', 'USDT', 'PAUSED', 'legacy')`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO t_logical_account_targets
 (c_space_id, c_logical_account_id, c_target_id, c_runner_id, c_command_sequence,
  c_targets_json, c_status, c_blocked_targets_json, c_accepted_at)
VALUES ('space', 'logical', 'target-1', 'runner', 1, '[]', 'PENDING', '[]', 1)`).Error)
	require.NoError(t, db.Exec(`
INSERT INTO t_logical_account_target_receipts
 (c_space_id, c_target_id, c_runner_id, c_logical_account_id, c_command_sequence,
  c_request_hash, c_signal_time, c_weights_json, c_equity, c_equity_source_time,
  c_reference_prices_json, c_quantity_targets_json, c_accepted_at)
VALUES ('space', 'target-1', 'runner', 'logical', 1, 'hash', 1, '[]', '1', 1, '{}', '[]', 1)`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	s, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	for _, table := range []string{"t_logical_account_targets", "t_logical_account_target_receipts"} {
		var count int64
		require.NoError(t, s.db.Raw(`SELECT COUNT(*) FROM `+table).Scan(&count).Error)
		require.Equal(t, int64(1), count, table)
		var columns []string
		require.NoError(t, s.db.Raw(`SELECT name FROM pragma_table_info(?)`, table).Scan(&columns).Error)
		require.Contains(t, columns, "c_instance_id")
	}
}
