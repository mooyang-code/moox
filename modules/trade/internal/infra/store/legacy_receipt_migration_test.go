package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const receiptFixtureSQL = `CREATE TABLE t_logical_account_target_receipts (
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
)`

func receiptMigrationDB(t *testing.T, ddl string) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "receipt.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(schema.AllSQL()).Error)
	require.NoError(t, db.Exec(`DROP TABLE t_logical_account_target_receipts; DROP TABLE t_logical_account_targets`).Error)
	require.NoError(t, db.Exec(legacyStrategyTargetTableSQL).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_accounts (c_space_id,c_logical_account_id,c_name,c_execution_mode,c_market_type,c_settlement_asset,c_automation_state,c_pause_reason) VALUES ('s','a','a','PAPER','SPOT','USDT','PAUSED','test')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_account_targets (c_space_id,c_logical_account_id,c_target_id,c_runner_id,c_command_sequence,c_targets_json,c_status,c_accepted_at) VALUES ('s','a','target','runner',1,'[]','PENDING',123)`).Error)
	require.NoError(t, db.Exec(ddl).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_account_target_receipts VALUES ('s','target','runner','a',1,'hash',100,'[{"symbol":"BTC","weight":"0.25"}]','123.45',99,'{"BTC":"10.5"}','[{"symbol":"BTC","quantity":"2"}]',123)`).Error)
	return db, path
}

func migrationDataSnapshot(t *testing.T, db *gorm.DB) map[string][]map[string]interface{} {
	t.Helper()
	var tables []string
	require.NoError(t, db.Raw(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`).Scan(&tables).Error)
	result := make(map[string][]map[string]interface{})
	for _, table := range tables {
		var rows []map[string]interface{}
		require.NoError(t, db.Raw(`SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`" ORDER BY rowid`).Scan(&rows).Error)
		result[table] = rows
	}
	return result
}

func TestLegacyReceiptMigrationRejectsUnknownShapeWithoutMutation(t *testing.T) {
	for name, change := range map[string]struct{ ddl, extension string }{
		"column":                         {receiptFixtureSQL, `ALTER TABLE t_logical_account_target_receipts ADD COLUMN c_extension TEXT DEFAULT 'retained'`},
		"instance only":                  {receiptFixtureSQL, `ALTER TABLE t_logical_account_target_receipts ADD COLUMN c_instance_id TEXT NOT NULL DEFAULT ''`},
		"index":                          {receiptFixtureSQL, `CREATE INDEX custom_receipt_index ON t_logical_account_target_receipts(c_runner_id)`},
		"altered known index":            {receiptFixtureSQL, `CREATE INDEX idx_target_receipts_logical ON t_logical_account_target_receipts(c_runner_id)`},
		"trigger":                        {receiptFixtureSQL, `CREATE TRIGGER custom_receipt_trigger AFTER DELETE ON t_logical_account_target_receipts BEGIN SELECT 1; END`},
		"incoming foreign key":           {receiptFixtureSQL, `CREATE TABLE receipt_children (space TEXT,target TEXT,FOREIGN KEY(space,target) REFERENCES t_logical_account_target_receipts(c_space_id,c_target_id) ON DELETE CASCADE); INSERT INTO receipt_children VALUES ('s','target')`},
		"check":                          {strings.Replace(receiptFixtureSQL, "CHECK (c_command_sequence > 0)", "CHECK (c_command_sequence > 0), CHECK (c_equity <> '')", 1), ""},
		"default":                        {strings.Replace(receiptFixtureSQL, "c_equity TEXT NOT NULL", "c_equity TEXT NOT NULL DEFAULT '9'", 1), ""},
		"unique":                         {strings.Replace(receiptFixtureSQL, "CHECK (c_command_sequence > 0)", "UNIQUE (c_request_hash), CHECK (c_command_sequence > 0)", 1), ""},
		"literal case":                   {"PRAGMA ignore_check_constraints=ON; " + strings.Replace(receiptFixtureSQL, "'array'", "'ARRAY'", 1), "PRAGMA ignore_check_constraints=OFF"},
		"literal quote":                  {"PRAGMA ignore_check_constraints=ON; " + strings.Replace(receiptFixtureSQL, "'array'", `'ar"ray'`, 1), "PRAGMA ignore_check_constraints=OFF"},
		"uppercase index":                {receiptFixtureSQL, `CREATE INDEX custom_receipt_index ON T_LOGICAL_ACCOUNT_TARGET_RECEIPTS(c_runner_id)`},
		"uppercase trigger":              {receiptFixtureSQL, `CREATE TRIGGER custom_receipt_trigger AFTER DELETE ON T_LOGICAL_ACCOUNT_TARGET_RECEIPTS BEGIN SELECT 1; END`},
		"uppercase incoming foreign key": {receiptFixtureSQL, `PRAGMA foreign_keys=ON; CREATE TABLE receipt_children (space TEXT,target TEXT,FOREIGN KEY(space,target) REFERENCES T_LOGICAL_ACCOUNT_TARGET_RECEIPTS(c_space_id,c_target_id) ON DELETE CASCADE); INSERT INTO receipt_children VALUES ('s','target')`},
		"uppercase table":                {strings.Replace(receiptFixtureSQL, "CREATE TABLE t_logical_account_target_receipts", "CREATE TABLE T_LOGICAL_ACCOUNT_TARGET_RECEIPTS", 1), ""},
	} {
		t.Run(name, func(t *testing.T) {
			db, path := receiptMigrationDB(t, change.ddl)
			if change.extension != "" {
				require.NoError(t, db.Exec(change.extension).Error)
			}
			beforeSchema, beforeData := migrationSchemaSnapshot(t, db), migrationDataSnapshot(t, db)
			require.ErrorIs(t, rebuildLegacyTargetReceiptTable(db), ErrIncompatibleSchema)
			require.Equal(t, beforeSchema, migrationSchemaSnapshot(t, db))
			require.Equal(t, beforeData, migrationDataSnapshot(t, db))
			s, err := Open(path)
			if s != nil {
				_ = s.Close()
			}
			require.ErrorIs(t, err, ErrIncompatibleSchema)
			require.Equal(t, beforeSchema, migrationSchemaSnapshot(t, db))
			require.Equal(t, beforeData, migrationDataSnapshot(t, db))
			require.False(t, tableHasColumn(db, "t_logical_account_targets", "c_instance_id"), "earlier target rebuild must roll back")
		})
	}
}

func TestLegacyReceiptMigrationPreservesFactsAndReopens(t *testing.T) {
	for _, variant := range []struct{ indexed, commented bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(variant), func(t *testing.T) {
			ddl := receiptFixtureSQL
			if variant.commented {
				ddl = strings.Replace(ddl, "    UNIQUE (", `    -- The runner is part of the sequence namespace. command_sequence remains
    -- monotonic for a runner, while target_id is the replay/idempotency key
    -- for one accepted command.
    UNIQUE (`, 1)
			}
			db, path := receiptMigrationDB(t, ddl)
			if variant.indexed {
				require.NoError(t, db.Exec(`CREATE INDEX idx_target_receipts_logical ON t_logical_account_target_receipts (c_space_id, c_logical_account_id, c_accepted_at)`).Error)
			}
			before := migrationDataSnapshot(t, db)["t_logical_account_target_receipts"]
			for i := 0; i < 2; i++ {
				s, err := Open(path)
				require.NoError(t, err)
				require.NoError(t, s.Close())
				after := migrationDataSnapshot(t, db)["t_logical_account_target_receipts"]
				for _, row := range after {
					for _, col := range []string{"c_instance_id", "c_session_id", "c_strategy_id"} {
						require.Equal(t, "", row[col])
						delete(row, col)
					}
					for _, col := range []string{"c_bar_end_time", "c_effective_at", "c_valid_until"} {
						require.EqualValues(t, 0, row[col])
						delete(row, col)
					}
				}
				require.Equal(t, before, after)
			}
		})
	}
}

func TestLegacyReceiptMigrationDDLFailureRollsBack(t *testing.T) {
	db, _ := receiptMigrationDB(t, receiptFixtureSQL)
	beforeSchema, beforeData := migrationSchemaSnapshot(t, db), migrationDataSnapshot(t, db)
	injected := errors.New("injected receipt rename failure")
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("reject_receipt_rename", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "ALTER TABLE t_logical_account_target_receipts__new RENAME") {
			tx.AddError(injected)
		}
	}))
	require.ErrorIs(t, rebuildLegacyTargetReceiptTable(db), injected)
	require.NoError(t, db.Callback().Raw().Remove("reject_receipt_rename"))
	require.Equal(t, beforeSchema, migrationSchemaSnapshot(t, db))
	require.Equal(t, beforeData, migrationDataSnapshot(t, db))
}

func TestReceiptSchemaTokensRespectCommentBoundaries(t *testing.T) {
	tokens := func(ddl string) [][]string {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		defer sqlDB.Close()
		require.NoError(t, db.Exec(ddl).Error)
		result, err := receiptSchemaTokens(db)
		require.NoError(t, err)
		return result
	}
	active := tokens("CREATE TABLE t_logical_account_target_receipts (c_id INTEGER CHECK (c_id > 0) -- marker\n CHECK (c_id < 10)\n)")
	commented := tokens("CREATE TABLE t_logical_account_target_receipts (c_id INTEGER CHECK (c_id > 0) -- marker CHECK (c_id < 10)\n)")
	require.NotEqual(t, active, commented, "moving a newline must not hide an active constraint")
	plain := tokens("CREATE TABLE t_logical_account_target_receipts (c_id INTEGER CHECK (c_id > 0))")
	block := tokens("CREATE TABLE t_logical_account_target_receipts (c_id INTEGER /* 'quoted comment' */ CHECK (c_id > 0))")
	require.Equal(t, plain, block)
}
