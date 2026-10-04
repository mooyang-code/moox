package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOpenLegacyPinnedCutoverRollsBackAfterPaperFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-pinned.db")
	s, err := Open(path)
	require.NoError(t, err)
	fill := seedPaperBalanceOrder(t, s)
	require.NoError(t, s.Transaction(context.Background(), func(tx *Tx) error {
		_, err := tx.InsertFill(fill)
		return err
	}))
	require.NoError(t, s.Close())

	// Build an old identity/target schema around real historical Paper facts.
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := raw.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	for _, statement := range []string{
		"DROP TABLE t_logical_account_targets",
		"DROP TABLE t_logical_accounts",
		legacyLogicalAccountTableSQL,
		`CREATE UNIQUE INDEX ux_logical_account_owner_runner ON t_logical_accounts (c_space_id, c_owner_runner_id) WHERE c_owner_runner_id IS NOT NULL`,
		legacyStrategyTargetTableSQL,
		`INSERT INTO t_logical_accounts (c_space_id, c_logical_account_id, c_name, c_owner_runner_id, c_execution_mode, c_market_type, c_settlement_asset, c_automation_state, c_pause_reason)
VALUES ('space-1', 'logical-1', 'legacy', 'runner-1', 'PAPER', 'SPOT', 'USDT', 'ACTIVE', '')`,
		"DROP TABLE t_paper_asset_balances",
		"DROP TABLE t_paper_balance_projections",
		"UPDATE t_order_fills SET c_fee = ''",
	} {
		require.NoError(t, raw.Exec(statement).Error)
	}
	require.NoError(t, raw.Exec(`INSERT INTO t_logical_account_targets
(c_space_id, c_logical_account_id, c_target_id, c_runner_id, c_command_sequence, c_targets_json, c_status, c_accepted_at)
VALUES ('space-1', 'logical-1', 'target-1', 'runner-1', 1, ?, 'PENDING', 1)`, pinnedTargetJSON).Error)
	before := snapshotStartupDatabase(t, raw)
	for attempt := 0; attempt < 2; attempt++ {
		opened, err := Open(path)
		if opened != nil {
			require.NoError(t, opened.Close())
		}
		require.ErrorIs(t, err, ErrInvalidRecord)
		require.ErrorContains(t, err, "initialize paper balances")
		require.Equal(t, before, snapshotStartupDatabase(t, raw), "late failure must roll back old table rebuild, fence initialization, pin invalidation, and new balance tables")
	}

	// Repair only the deliberately damaged fixture. The same migration must
	// now succeed and preserve the actual order/fill while invalidating the pin.
	require.NoError(t, raw.Exec("UPDATE t_order_fills SET c_fee = ?", fill.Fee).Error)
	var factsBefore []map[string]interface{}
	require.NoError(t, raw.Table("t_order_fills").Find(&factsBefore).Error)
	opened, err := Open(path)
	require.NoError(t, err)
	account, err := opened.GetLogicalAccount(context.Background(), "space-1", "logical-1")
	require.NoError(t, err)
	require.Equal(t, "PAUSED", account.AutomationState)
	require.Equal(t, TargetPinMigrationPauseReason, account.PauseReason)
	require.Empty(t, account.OwnerRunnerID)
	require.Empty(t, account.OwnerInstanceID)
	require.Empty(t, account.OwnerSessionID)
	require.NotEmpty(t, account.AuthFence)
	_, err = opened.GetLogicalAccountTarget(context.Background(), "space-1", "logical-1")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	balance, err := opened.GetPaperBalanceSnapshot(context.Background(), fill.SpaceID, fill.TradingAccountID)
	require.NoError(t, err)
	require.Equal(t, "99899.9", balance.Totals["USDT"].String())
	require.Equal(t, "1", balance.Totals["BTC"].String())
	require.Equal(t, int64(1), balance.AppliedFillCount)
	var factsAfter []map[string]interface{}
	require.NoError(t, opened.db.Table("t_order_fills").Find(&factsAfter).Error)
	require.Equal(t, factsBefore, factsAfter)
	require.NoError(t, opened.Close())
	after := snapshotStartupDatabase(t, raw)
	opened, err = Open(path)
	require.NoError(t, err)
	require.NoError(t, opened.Close())
	require.Equal(t, after, snapshotStartupDatabase(t, raw), "successful cutover is idempotent on reopen")
}

type startupDatabaseSnapshot struct {
	Schema []struct {
		Type string
		Name string
		SQL  *string
	}
	Rows map[string][]map[string]interface{}
}

func snapshotStartupDatabase(t *testing.T, db *gorm.DB) startupDatabaseSnapshot {
	t.Helper()
	result := startupDatabaseSnapshot{Rows: map[string][]map[string]interface{}{}}
	require.NoError(t, db.Raw("SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name").Scan(&result.Schema).Error)
	for _, item := range result.Schema {
		if item.Type != "table" {
			continue
		}
		var rows []map[string]interface{}
		require.NoError(t, db.Table(item.Name).Order("rowid").Find(&rows).Error)
		result.Rows[item.Name] = rows
	}
	return result
}
