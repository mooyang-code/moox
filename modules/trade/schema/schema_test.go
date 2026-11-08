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
		"t_trading_accounts",
		"t_trade_instruments",
		"t_trading_position_tombstones",
		"t_trading_positions",
		"t_logical_account_members",
		"t_logical_account_targets",
		"t_logical_accounts",
		"t_operator_actions",
		"t_order_fills",
		"t_trade_orders",
		"t_paper_account_configs",
		"t_paper_balance_projections",
		"t_shadow_targets",
		"t_replay_sessions",
		"t_replay_periods",
		"t_replay_orders",
		"t_paper_asset_balances",
		"t_paper_funding_fees",
		"t_account_equity_points",
		"t_logical_account_equity_points",
		"t_logical_account_target_receipts",
		"t_trade_wallet_transfers",
		"t_trade_blocked_instruments",
	}
	sort.Strings(want)
	require.Equal(t, want, tables)
}

func TestEachSQLLoadsIntoEmptyDatabase(t *testing.T) {
	for name, sql := range map[string]string{
		"account.sql": accountSQL, "instrument.sql": instrumentSQL,
		"logical_account.sql": logicalAccountSQL, "paper_account_config.sql": paperAccountConfigSQL,
		"paper_balance.sql": paperBalanceSQL, "paper_funding.sql": paperFundingSQL, "equity.sql": equitySQL,
		"target_receipt.sql": targetReceiptSQL, "execution.sql": executionSQL,
		"wallet_transfer.sql": walletTransferSQL, "blocked_instrument.sql": blockedInstrumentSQL,
	} {
		t.Run(name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			require.NoError(t, db.Exec(sql).Error)
		})
	}
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
		{"t_trading_accounts", []string{"c_space_id", "c_trading_account_id"}},
		{"t_trading_accounts", []string{"c_space_id", "c_name"}},
		{"t_trade_instruments", []string{"c_exchange", "c_environment", "c_market_type", "c_exchange_symbol"}},
		{"t_trade_orders", []string{"c_space_id", "c_order_id"}},
		{"t_trade_orders", []string{"c_space_id", "c_trading_account_id", "c_market_type", "c_client_order_id"}},
		{"t_order_fills", []string{"c_space_id", "c_trading_account_id", "c_market_type", "c_exchange_symbol", "c_exchange_trade_id"}},
		{"t_trading_positions", []string{"c_space_id", "c_trading_account_id", "c_market_type", "c_exchange_symbol", "c_position_side"}},
		{"t_trading_position_tombstones", []string{"c_space_id", "c_trading_account_id", "c_market_type", "c_exchange_symbol", "c_position_side"}},
		{"t_trade_wallet_transfers", []string{"c_space_id", "c_transfer_id"}},
		{"t_trade_wallet_transfers", []string{"c_space_id", "c_trading_account_id", "c_client_ref"}},
		{"t_trade_blocked_instruments", []string{"c_exchange", "c_market_type", "c_exchange_symbol"}},
		{"t_trading_accounts", []string{"c_exchange", "c_live_environment", "c_credential_secret_id"}},
		{"t_logical_accounts", []string{"c_space_id", "c_logical_account_id"}},
		{"t_logical_accounts", []string{"c_space_id", "c_name"}},
		{"t_logical_accounts", []string{"c_space_id", "c_owner_instance_id"}},
		{"t_logical_account_members", []string{"c_space_id", "c_logical_account_id", "c_trading_account_id"}},
		{"t_logical_account_members", []string{"c_space_id", "c_trading_account_id"}},
		{"t_logical_account_targets", []string{"c_space_id", "c_logical_account_id"}},
		{"t_logical_account_targets", []string{"c_space_id", "c_target_id"}},
		{"t_operator_actions", []string{"c_space_id", "c_action_id"}},
		{"t_paper_balance_projections", []string{"c_space_id", "c_trading_account_id"}},
		{"t_paper_asset_balances", []string{"c_space_id", "c_trading_account_id", "c_asset"}},
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
	require.Contains(t, sql, "c_live_environment TEXT NOT NULL")
	require.Contains(t, sql, "c_execution_mode = 'PAPER'")
	require.Contains(t, sql, "c_automation_state TEXT NOT NULL")
	require.NotContains(t, sql, "c_control_state")
	require.NotContains(t, sql, "c_control_revision")
	require.NotContains(t, sql, "c_paused INTEGER")
	require.Contains(t, sql, "'REDUCE'")
	require.Contains(t, sql, "'DELIST_FLATTEN'")
	require.NotContains(t, sql, "c_market_type TEXT NOT NULL,\n    c_settlement_asset")
}

func TestTradingAccountSchemaRestrictsMarketsAndCredentialUse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)

	insert := func(id, markets, mode, status string) error {
		return db.Exec(`
			INSERT INTO t_trading_accounts (
				c_space_id, c_trading_account_id, c_name, c_exchange, c_markets,
				c_execution_mode, c_live_environment, c_credential_secret_id,
				c_settlement_asset, c_account_mode, c_status
			) VALUES ('space', ?, ?, 'BINANCE', ?, 'LIVE', 'PRODUCTION', 'secret-1', 'USDT', ?, ?)
		`, id, id, markets, mode, status).Error
	}
	require.NoError(t, insert("a1", "SPOT,SWAP", "BINANCE_STANDARD", "ENABLED"))
	// 同一把密钥只能有一个启用的实盘账户，跨空间也一样；停用的账户不占用。
	require.Error(t, insert("a2", "SPOT", "", "ENABLED"))
	require.NoError(t, insert("a3", "SPOT", "", "DISABLED"))
	require.Error(t, db.Exec(`UPDATE t_trading_accounts SET c_status = 'ENABLED' WHERE c_trading_account_id = 'a3'`).Error)
	require.Error(t, db.Exec(`
		INSERT INTO t_trading_accounts (
			c_space_id, c_trading_account_id, c_name, c_exchange, c_markets,
			c_execution_mode, c_live_environment, c_credential_secret_id,
			c_settlement_asset, c_status
		) VALUES ('other-space', 'a4', 'a4', 'BINANCE', 'SPOT', 'LIVE', 'PRODUCTION', 'secret-1', 'USDT', 'ENABLED')
	`).Error)
	// 市场集合必须是有序的合法写法，账户类型只给加密货币实盘账户。
	require.Error(t, insert("a5", "SWAP,SPOT", "", "DISABLED"))
	require.Error(t, insert("a6", "STOCK", "OKX_UNIFIED", "DISABLED"))
	require.Error(t, insert("a7", "SPOT", "UNKNOWN_MODE", "DISABLED"))
}

func TestExecutionSchemaKeysIncludeMarket(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = OFF`).Error)

	// 币安现货和合约的代码同名，订单号、成交号是两套独立序列：不同市场的记录必须能共存，同市场重复仍被拒绝。
	for _, market := range []string{"SPOT", "SWAP"} {
		require.NoError(t, db.Exec(`
			INSERT INTO t_trade_orders (
				c_space_id, c_order_id, c_trading_account_id, c_client_order_id, c_exchange_order_id,
				c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_order_type, c_side,
				c_quantity, c_reference_price, c_reference_price_at, c_owner_type, c_owner_id, c_state
			) VALUES ('s', ?, 'a', ?, '100', 'BINANCE', ?, 'BTC-USDT', 'BTCUSDT', 'MARKET', 'BUY',
				'1', '1', 1, 'EXTERNAL', 'x', 'OPEN')
		`, "order-"+market, "client-"+market, market).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO t_order_fills (
				c_space_id, c_fill_id, c_exchange_trade_id, c_order_id, c_trading_account_id,
				c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_side, c_price,
				c_quantity, c_traded_at
			) VALUES ('s', ?, '7', ?, 'a', 'BINANCE', ?, 'BTC-USDT', 'BTCUSDT', 'BUY', '1', '1', 1)
		`, "fill-"+market, "order-"+market, market).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO t_trading_positions (c_space_id, c_trading_account_id, c_market_type, c_instrument_id, c_exchange_symbol, c_position_side)
			VALUES ('s', 'a', ?, 'BTC-USDT', 'BTCUSDT', 'NET')
		`, market).Error)
		require.NoError(t, db.Exec(`
			INSERT INTO t_trading_position_tombstones (c_space_id, c_trading_account_id, c_market_type, c_exchange_symbol, c_position_side)
			VALUES ('s', 'a', ?, 'BTCUSDT', 'NET')
		`, market).Error)
	}
	require.Error(t, db.Exec(`
		INSERT INTO t_trade_orders (
			c_space_id, c_order_id, c_trading_account_id, c_client_order_id, c_exchange_order_id,
			c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_order_type, c_side,
			c_quantity, c_reference_price, c_reference_price_at, c_owner_type, c_owner_id, c_state
		) VALUES ('s', 'order-dup', 'a', 'client-dup', '100', 'BINANCE', 'SPOT', 'BTC-USDT', 'BTCUSDT',
			'MARKET', 'BUY', '1', '1', 1, 'EXTERNAL', 'x', 'OPEN')
	`).Error)
	require.Error(t, db.Exec(`
		INSERT INTO t_order_fills (
			c_space_id, c_fill_id, c_exchange_trade_id, c_order_id, c_trading_account_id,
			c_exchange, c_market_type, c_instrument_id, c_exchange_symbol, c_side, c_price,
			c_quantity, c_traded_at
		) VALUES ('s', 'fill-dup', '7', 'order-SPOT', 'a', 'BINANCE', 'SPOT', 'BTC-USDT', 'BTCUSDT', 'BUY', '1', '1', 1)
	`).Error)
}

func TestOrderSchemaAcceptsSystemOwnerOnlyWithLogicalAccount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = OFF`).Error)

	insert := func(logicalAccountID any, runnerID any) error {
		return db.Exec(`
			INSERT INTO t_trade_orders (
				c_space_id, c_order_id, c_trading_account_id, c_client_order_id, c_exchange,
				c_market_type, c_instrument_id, c_exchange_symbol, c_order_type, c_side, c_quantity,
				c_reference_price, c_reference_price_at, c_owner_type, c_owner_id,
				c_logical_account_id, c_runner_id, c_state
			) VALUES ('s', lower(hex(randomblob(4))), 'a', lower(hex(randomblob(4))), 'BINANCE',
				'SPOT', 'BNB-USDT', 'BNBUSDT', 'MARKET', 'BUY', '1', '1', 1, 'SYSTEM', 'bnb-topup', ?, ?, 'OPEN')
		`, logicalAccountID, runnerID).Error
	}
	require.NoError(t, insert("logical-1", nil))
	require.Error(t, insert(nil, nil))
	require.Error(t, insert("logical-1", "runner-1"))
}

func TestNewTablesRejectInvalidRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(AllSQL()).Error)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = OFF`).Error)

	require.NoError(t, db.Exec(`
		INSERT INTO t_trade_blocked_instruments (c_exchange, c_market_type, c_exchange_symbol, c_reason, c_blocked_at)
		VALUES ('BINANCE', 'SPOT', 'FOOUSDT', '交易所公告下架', 1)
	`).Error)
	require.Error(t, db.Exec(`
		INSERT INTO t_trade_blocked_instruments (c_exchange, c_market_type, c_exchange_symbol, c_reason, c_blocked_at)
		VALUES ('BINANCE', 'SPOT', 'FOOUSDT', '重复', 2)
	`).Error)
	// 同名代码在另一个市场是另一条记录。
	require.NoError(t, db.Exec(`
		INSERT INTO t_trade_blocked_instruments (c_exchange, c_market_type, c_exchange_symbol, c_reason, c_blocked_at)
		VALUES ('BINANCE', 'SWAP', 'FOOUSDT', '永续下架', 3)
	`).Error)

	transfer := func(id, direction, state string) error {
		return db.Exec(`
			INSERT INTO t_trade_wallet_transfers (
				c_space_id, c_transfer_id, c_trading_account_id, c_logical_account_id, c_target_id,
				c_direction, c_asset, c_amount, c_client_ref, c_state, c_requested_at
			) VALUES ('s', ?, 'a', 'l', 'target-1', ?, 'USDT', '100', ?, ?, 1)
		`, id, direction, "ref-"+id, state).Error
	}
	require.NoError(t, transfer("t1", "TO_SPOT", "PENDING"))
	require.NoError(t, transfer("t2", "TO_FUTURES", "COMPLETED"))
	require.Error(t, transfer("t3", "WITHDRAW", "PENDING"))
	require.Error(t, transfer("t4", "TO_SPOT", "UNKNOWN"))
	require.Error(t, db.Exec(`
		INSERT INTO t_trade_wallet_transfers (
			c_space_id, c_transfer_id, c_trading_account_id, c_logical_account_id, c_target_id,
			c_direction, c_asset, c_amount, c_client_ref, c_capped, c_state, c_requested_at
		) VALUES ('s', 't5', 'a', 'l', 'target-1', 'TO_SPOT', 'USDT', '100', 'ref-t5', 2, 'PENDING', 1)
	`).Error, "封顶标记只能是 0 或 1")
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
