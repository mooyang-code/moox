package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func controlMigrationDB(t *testing.T, ddl string) (*gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(ddl).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_accounts
(c_space_id,c_logical_account_id,c_name,c_owner_runner_id,c_owner_instance_id,c_owner_session_id,c_auth_fence,c_owner_claimed_at,c_execution_mode,c_market_type,c_settlement_asset,c_automation_state,c_pause_reason)
VALUES ('space-1','logical-1','original','runner-1','instance-1','session-1','fence-1',5,'PAPER','SPOT','USDT','PAUSED','original')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_operator_actions
(c_space_id,c_action_id,c_logical_account_id,c_action_type,c_reason,c_request_json,c_status,c_result_json)
VALUES ('space-1','action-1','logical-1','MANUAL_ORDER','original','{}','COMPLETED','{"order_id":"paper-order"}')`).Error)
	return db, path
}

func TestControlModeMigrationPreservesHistoricalFacts(t *testing.T) {
	db, path := controlMigrationDB(t, preControlModeSQL())
	s := &Store{db: db}
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	require.NoError(t, db.Exec(`INSERT INTO t_logical_account_members (c_space_id,c_logical_account_id,c_trading_account_id) VALUES ('space-1','logical-1','account-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_account_targets
(c_space_id,c_logical_account_id,c_target_id,c_runner_id,c_command_sequence,c_targets_json,c_status,c_accepted_at)
VALUES ('space-1','logical-1','target-1','runner-1',1,'[]','PENDING',123)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_logical_account_target_receipts
(c_space_id,c_target_id,c_runner_id,c_logical_account_id,c_command_sequence,c_request_hash,c_signal_time,c_weights_json,c_equity,c_equity_source_time,c_reference_prices_json,c_quantity_targets_json,c_accepted_at)
VALUES ('space-1','target-1','runner-1','logical-1',1,'hash',123,'[]','1000',123,'{}','[]',123)`).Error)
	before := snapshotStartupDatabase(t, db).Rows
	for attempt := 0; attempt < 2; attempt++ {
		opened, err := Open(path)
		require.NoError(t, err)
		after := snapshotStartupDatabase(t, opened.db).Rows
		for _, row := range after["t_logical_accounts"] {
			require.Equal(t, "STRATEGY", row["c_control_mode"])
			delete(row, "c_control_mode")
		}
		require.Equal(t, before, after)
		var fkErrors []map[string]interface{}
		require.NoError(t, opened.db.Raw("PRAGMA foreign_key_check").Scan(&fkErrors).Error)
		require.Empty(t, fkErrors)
		require.NoError(t, opened.Close())
	}
	require.NoError(t, db.Exec(`INSERT INTO t_operator_actions (c_space_id,c_action_id,c_logical_account_id,c_action_type,c_reason,c_request_json,c_status) VALUES ('space-1','submit-1','logical-1','SUBMIT_ORDER','new','{}','RUNNING')`).Error)
}

func TestControlModeMigrationRejectsUnknownShapesAtomically(t *testing.T) {
	changeLogical := func(old, replacement string) string {
		ddl := preControlModeSQL()
		start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS t_logical_accounts (")
		require.NotEqual(t, -1, start)
		return ddl[:start] + strings.Replace(ddl[start:], old, replacement, 1)
	}
	for name, change := range map[string]struct{ ddl, extra string }{
		"logical literal":     {changeLogical("CHECK (c_execution_mode IN ('PAPER', 'LIVE'))", "CHECK (c_execution_mode IN ('PAPER', 'live'))"), ""},
		"action literal":      {strings.Replace(preControlModeSQL(), "'MANUAL_ORDER', 'CANCEL_ORDER', 'FLATTEN'", "'MANUAL_ORDER', 'CANCEL_ORDER', 'flatten'", 1), ""},
		"logical check":       {changeLogical("CHECK (c_execution_mode IN ('PAPER', 'LIVE'))", "CHECK (c_execution_mode IN ('PAPER', 'LIVE')), CHECK (c_name <> '')"), ""},
		"logical mode column": {preControlModeSQL(), "ALTER TABLE t_logical_accounts ADD COLUMN c_control_mode TEXT NOT NULL DEFAULT 'MANUAL'"},
		"logical extension":   {preControlModeSQL(), "ALTER TABLE t_logical_accounts ADD COLUMN c_unknown TEXT"},
		"logical trigger":     {preControlModeSQL(), "CREATE TRIGGER unknown_logical AFTER UPDATE ON T_LOGICAL_ACCOUNTS BEGIN SELECT 1; END"},
		"action check":        {strings.Replace(preControlModeSQL(), "CHECK (c_action_type IN ('MANUAL_ORDER', 'CANCEL_ORDER', 'FLATTEN'))", "CHECK (c_action_type IN ('MANUAL_ORDER', 'CANCEL_ORDER', 'FLATTEN')), CHECK(c_reason <> '')", 1), ""},
		"action extension":    {preControlModeSQL(), "ALTER TABLE t_operator_actions ADD COLUMN c_unknown TEXT DEFAULT 'retained'"},
		"action index":        {preControlModeSQL(), "CREATE INDEX unknown_action ON T_OPERATOR_ACTIONS(c_reason)"},
		"action trigger":      {preControlModeSQL(), "CREATE TRIGGER unknown_action AFTER DELETE ON T_OPERATOR_ACTIONS BEGIN SELECT 1; END"},
		"action child":        {preControlModeSQL(), "PRAGMA foreign_keys=ON; CREATE TABLE action_child (space TEXT, id TEXT, FOREIGN KEY(space,id) REFERENCES T_OPERATOR_ACTIONS(c_space_id,c_action_id) ON DELETE CASCADE); INSERT INTO action_child VALUES ('space-1','action-1')"},
	} {
		t.Run(name, func(t *testing.T) {
			db, path := controlMigrationDB(t, change.ddl)
			if change.extra != "" {
				require.NoError(t, db.Exec(change.extra).Error)
			}
			before := snapshotStartupDatabase(t, db)
			opened, err := Open(path)
			if opened != nil {
				require.NoError(t, opened.Close())
			}
			require.ErrorIs(t, err, ErrIncompatibleSchema)
			require.Equal(t, before, snapshotStartupDatabase(t, db))
		})
	}
}

func TestControlModeMigrationLateFailureRollsBack(t *testing.T) {
	db, path := controlMigrationDB(t, preControlModeSQL())
	s := &Store{db: db}
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error { _, err := tx.InsertFill(fill); return err }))
	require.NoError(t, db.Exec("UPDATE t_order_fills SET c_fee = ''; DROP TABLE t_paper_asset_balances; DROP TABLE t_paper_balance_projections").Error)
	before := snapshotStartupDatabase(t, db)
	for attempt := 0; attempt < 2; attempt++ {
		opened, err := Open(path)
		if opened != nil {
			require.NoError(t, opened.Close())
		}
		require.ErrorIs(t, err, ErrInvalidRecord)
		require.ErrorContains(t, err, "initialize paper balances")
		require.Equal(t, before, snapshotStartupDatabase(t, db))
	}
}

func TestControlModeMigrationRecognizesHistoricalAuthorizationLayouts(t *testing.T) {
	for _, generationAtEnd := range []bool{false, true} {
		for _, columns := range [][]string{
			{"c_owner_instance_id TEXT", "c_owner_session_id TEXT", "c_auth_fence TEXT NOT NULL DEFAULT ''"},
			{"c_owner_instance_id TEXT", "c_auth_fence TEXT NOT NULL DEFAULT ''", "c_owner_session_id TEXT"},
			{"c_owner_session_id TEXT", "c_owner_instance_id TEXT", "c_auth_fence TEXT NOT NULL DEFAULT ''"},
			{"c_owner_session_id TEXT", "c_auth_fence TEXT NOT NULL DEFAULT ''", "c_owner_instance_id TEXT"},
			{"c_auth_fence TEXT NOT NULL DEFAULT ''", "c_owner_instance_id TEXT", "c_owner_session_id TEXT"},
			{"c_auth_fence TEXT NOT NULL DEFAULT ''", "c_owner_session_id TEXT", "c_owner_instance_id TEXT"},
		} {
			db, path := controlMigrationDB(t, preControlModeSQL())
			require.NoError(t, db.Exec("PRAGMA foreign_keys=OFF").Error)
			require.NoError(t, db.Exec("DROP TABLE t_logical_accounts").Error)
			ddl := legacyLogicalAccountTableSQL
			if !generationAtEnd {
				ddl = strings.Replace(ddl, "    c_owner_runner_id TEXT,", "    c_owner_runner_id TEXT,\n    c_owner_claimed_at INTEGER NOT NULL DEFAULT 0,", 1)
			}
			require.NoError(t, db.Exec(ddl).Error)
			if generationAtEnd {
				require.NoError(t, db.Exec("ALTER TABLE t_logical_accounts ADD COLUMN c_owner_claimed_at INTEGER NOT NULL DEFAULT 0").Error)
			}
			for _, column := range columns {
				require.NoError(t, db.Exec("ALTER TABLE t_logical_accounts ADD COLUMN "+column).Error)
			}
			require.NoError(t, db.Exec(`CREATE UNIQUE INDEX ux_logical_account_owner_runner ON t_logical_accounts (c_space_id, c_owner_runner_id) WHERE c_owner_runner_id IS NOT NULL;
CREATE UNIQUE INDEX ux_logical_account_owner_instance ON t_logical_accounts (c_space_id, c_owner_instance_id) WHERE c_owner_instance_id IS NOT NULL;
INSERT INTO t_logical_accounts (c_space_id,c_logical_account_id,c_name,c_owner_claimed_at,c_owner_instance_id,c_owner_session_id,c_auth_fence,c_execution_mode,c_market_type,c_settlement_asset,c_automation_state,c_pause_reason)
VALUES ('space-1','logical-1','legacy',7,'instance-1','session-1','fence-1','PAPER','SPOT','USDT','PAUSED','legacy')`).Error)
			for attempt := 0; attempt < 2; attempt++ {
				opened, err := Open(path)
				require.NoError(t, err, "generationAtEnd=%t columns=%v", generationAtEnd, columns)
				account, err := opened.GetLogicalAccount(context.Background(), "space-1", "logical-1")
				require.NoError(t, err)
				require.Equal(t, "STRATEGY", account.ControlMode)
				require.Equal(t, int64(7), account.OwnerClaimedAt)
				require.Equal(t, "fence-1", account.AuthFence)
				require.NoError(t, opened.Close())
			}
		}
	}
}
