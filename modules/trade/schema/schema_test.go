package schema

import (
	"fmt"
	"sort"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAllSQLCreatesLogicalAccountTargetAndOperatorTablesWithoutLedger(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)

	var tables []string
	require.NoError(t, db.Raw(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name
	`).Scan(&tables).Error)

	want := []string{
		"t_exchange_accounts",
		"t_exchange_instruments",
		"t_exchange_positions",
		"t_logical_account_members",
		"t_logical_account_targets",
		"t_logical_accounts",
		"t_operator_actions",
		"t_order_fills",
		"t_trade_orders",
	}
	sort.Strings(want)
	require.Equal(t, want, tables)
}

func TestAllSQLHasNoRetiredTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)

	retired := []string{
		"t_trade_channels",
		"t_account_api_keys",
		"t_exchange_account_leverage",
		"t_exchange_account_snapshots",
		"t_target_positions",
		"t_trade_reservations",
		"t_trade_command_offsets",
		"t_trade_inbox",
		"t_execution_plans",
		"t_execution_slices",
		"t_trade_sagas",
		"t_rebalance_runs",
		"t_rebalance_legs",
		"t_trade_sync_cursors",
		"t_target_executions",
		"t_ledger_transactions",
		"t_ledger_entries",
		"t_trade_balance_projections",
	}
	for _, table := range retired {
		var count int64
		require.NoError(t, db.Raw(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count).Error)
		require.Zero(t, count, table)
	}
}

func TestAllSQLForeignKeysAndIndexesAreValid(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = ON`).Error)
	require.NoError(t, db.Exec(AllSQL()).Error)

	var violations []struct {
		Table string
		RowID int64
	}
	require.NoError(t, db.Raw(`PRAGMA foreign_key_check`).Scan(&violations).Error)
	require.Empty(t, violations)

	var indexCount int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'index' AND sql IS NOT NULL
	`).Scan(&indexCount).Error)
	require.GreaterOrEqual(t, indexCount, int64(10))
}

func TestAllSQLDefinesApprovedIdentityScopes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)

	tests := []struct {
		table   string
		columns []string
	}{
		{"t_exchange_accounts", []string{"c_space_id", "c_exchange_account_id"}},
		{"t_exchange_accounts", []string{"c_space_id", "c_name"}},
		{"t_exchange_instruments", []string{"c_exchange", "c_market_type", "c_symbol"}},
		{"t_trade_orders", []string{"c_space_id", "c_order_id"}},
		{"t_trade_orders", []string{"c_space_id", "c_exchange_account_id", "c_client_order_id"}},
		{"t_order_fills", []string{"c_space_id", "c_exchange_account_id", "c_symbol", "c_exchange_trade_id"}},
		{"t_exchange_positions", []string{"c_space_id", "c_exchange_account_id", "c_symbol", "c_position_side"}},
		{"t_logical_accounts", []string{"c_space_id", "c_logical_account_id"}},
		{"t_logical_accounts", []string{"c_space_id", "c_name"}},
		{"t_logical_accounts", []string{"c_space_id", "c_owner_runner_id"}},
		{"t_logical_account_members", []string{"c_space_id", "c_logical_account_id", "c_exchange_account_id"}},
		{"t_logical_account_members", []string{"c_space_id", "c_exchange_account_id"}},
		{"t_logical_account_targets", []string{"c_space_id", "c_logical_account_id"}},
		{"t_logical_account_targets", []string{"c_space_id", "c_target_id"}},
		{"t_operator_actions", []string{"c_space_id", "c_action_id"}},
	}
	for _, tt := range tests {
		require.True(t, hasUniqueIndex(t, db, tt.table, tt.columns),
			"%s must have unique identity %v", tt.table, tt.columns)
	}
}

func TestInstrumentSchemaUsesApprovedSwapQuantityFields(t *testing.T) {
	sql := AllSQL()
	for _, column := range []string{
		"c_linear",
		"c_contract_value",
		"c_contract_value_asset",
		"c_exchange_quantity_step",
		"c_min_exchange_quantity",
	} {
		require.Contains(t, sql, column)
	}
	for _, retired := range []string{
		"c_contract_size",
		"c_quantity_step",
		"c_min_quantity",
	} {
		require.NotContains(t, sql, "\n    "+retired+" ")
	}
}

func TestLogicalAccountSchemaKeepsSettlementAssetAndUsesAutomationState(t *testing.T) {
	sql := AllSQL()
	require.Contains(t, sql, "c_settlement_asset TEXT NOT NULL")
	require.Contains(t, sql, "c_environment TEXT NOT NULL")
	require.Contains(t, sql, "'PAPER', 'TESTNET', 'PRODUCTION'")
	require.Contains(t, sql, "c_automation_state TEXT NOT NULL")
	require.NotContains(t, sql, "c_control_state")
	require.NotContains(t, sql, "c_control_revision")
	require.NotContains(t, sql, "c_paused INTEGER")
	require.Contains(
		t,
		sql,
		"'MANUAL_ORDER', 'CANCEL_ORDER', 'FLATTEN'",
	)
}

func hasUniqueIndex(t *testing.T, db *gorm.DB, table string, want []string) bool {
	t.Helper()
	var indexes []struct {
		Name   string `gorm:"column:name"`
		Unique int    `gorm:"column:unique"`
	}
	require.NoError(t, db.Raw(fmt.Sprintf(`PRAGMA index_list(%q)`, table)).Scan(&indexes).Error)
	for _, index := range indexes {
		if index.Unique != 1 {
			continue
		}
		var columns []struct {
			Name string `gorm:"column:name"`
		}
		require.NoError(t,
			db.Raw(fmt.Sprintf(`PRAGMA index_info(%q)`, index.Name)).Scan(&columns).Error)
		got := make([]string, 0, len(columns))
		for _, column := range columns {
			got = append(got, column.Name)
		}
		if fmt.Sprint(got) == fmt.Sprint(want) {
			return true
		}
	}
	return false
}
