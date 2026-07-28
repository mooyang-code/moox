// Package store owns the single SQLite persistence boundary for Trade.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"gorm.io/gorm"
)

const sqliteBusyTimeoutMS = 5000

var (
	ErrConflict           = errors.New("trade store: conflict")
	ErrInvalidRecord      = errors.New("trade store: invalid record")
	ErrIncompatibleSchema = errors.New("trade store: incompatible schema")
)

type Store struct {
	db           *gorm.DB
	accountLocks sync.Map
	targetLocks  sync.Map
}

type Tx struct {
	db *gorm.DB
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf(
		"%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(%d)",
		path,
		sqliteBusyTimeoutMS,
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open trade store: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access trade database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := validateExistingTradeSchema(db); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("apply trade schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) LockExchangeAccount(exchangeAccountID string) func() {
	value, _ := s.accountLocks.LoadOrStore(exchangeAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (s *Store) LockTargetBinding(spaceID string, executionBindingID string) func() {
	key := spaceID + "\x00" + executionBindingID
	value, _ := s.targetLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func validateExistingTradeSchema(db *gorm.DB) error {
	var tables []string
	if err := db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	`).Scan(&tables).Error; err != nil {
		return fmt.Errorf("inspect trade schema: %w", err)
	}
	obsolete := map[string]struct{}{
		"t_accounts": {}, "t_account_balances": {}, "t_account_fund_flows": {},
		"t_account_api_keys": {}, "t_trade_channels": {}, "t_order_operations": {},
		"t_exchange_account_leverage": {}, "t_exchange_account_snapshots": {},
		"t_target_positions": {}, "t_trade_reservations": {},
		"t_trade_command_offsets": {}, "t_trade_inbox": {},
		"t_execution_plans": {}, "t_execution_slices": {}, "t_trade_sagas": {},
		"t_rebalance_runs": {}, "t_rebalance_legs": {}, "t_rebalance_targets": {},
		"t_trade_sync_cursors": {}, "t_trade_order_aggregates": {},
		"t_trade_fill_events": {}, "t_trade_position_projections": {},
		"t_trade_controls": {}, "t_reconciliation_runs": {},
		"t_reconciliation_differences": {},
	}
	approved := map[string]struct{}{
		"t_exchange_accounts": {}, "t_exchange_instruments": {},
		"t_trade_orders": {}, "t_order_fills": {}, "t_exchange_positions": {},
		"t_target_executions": {}, "t_ledger_transactions": {},
		"t_ledger_entries": {}, "t_trade_balance_projections": {},
	}
	for _, table := range tables {
		if _, found := obsolete[table]; found {
			return fmt.Errorf("%w: obsolete table %s", ErrIncompatibleSchema, table)
		}
		if tradeOwnedTable(table) {
			if _, found := approved[table]; !found {
				return fmt.Errorf("%w: unexpected Trade table %s", ErrIncompatibleSchema, table)
			}
		}
	}
	if containsString(tables, "t_exchange_accounts") {
		var accountColumns []struct {
			Name string `gorm:"column:name"`
		}
		if err := db.Raw(`PRAGMA table_info("t_exchange_accounts")`).
			Scan(&accountColumns).Error; err != nil {
			return fmt.Errorf("inspect Exchange account schema: %w", err)
		}
		got := make([]string, 0, len(accountColumns))
		for _, column := range accountColumns {
			got = append(got, column.Name)
		}
		sort.Strings(got)
		want := []string{
			"c_credential_secret_id", "c_ctime", "c_exchange",
			"c_exchange_account_id", "c_execution_mode", "c_fill_cursors_json",
			"c_last_error", "c_last_ready_at", "c_last_sync_at",
			"c_leverage_settings_json", "c_margin_mode", "c_market_type",
			"c_mtime", "c_name", "c_pause_reason", "c_paused", "c_ready",
			"c_settlement_asset", "c_snapshot_json", "c_snapshot_source_time",
			"c_space_id", "c_status", "c_sync_symbols_json",
		}
		sort.Strings(want)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			return fmt.Errorf(
				"%w: t_exchange_accounts columns do not match current schema",
				ErrIncompatibleSchema,
			)
		}
	}
	if !containsString(tables, "t_target_executions") {
		return nil
	}
	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(`PRAGMA table_info("t_target_executions")`).Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect target execution schema: %w", err)
	}
	got := make([]string, 0, len(columns))
	for _, column := range columns {
		got = append(got, column.Name)
	}
	sort.Strings(got)
	want := []string{
		"c_command_sequence", "c_ctime", "c_data_revision", "c_event_id",
		"c_exchange_account_id", "c_execution_binding_id", "c_execution_id",
		"c_last_error", "c_mtime", "c_not_after", "c_progress",
		"c_residual_quantity", "c_space_id", "c_status", "c_strategy_run_id",
		"c_targets_json",
	}
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		return fmt.Errorf(
			"%w: t_target_executions columns do not match current schema",
			ErrIncompatibleSchema,
		)
	}
	return nil
}

func tradeOwnedTable(table string) bool {
	for _, prefix := range []string{
		"t_exchange_", "t_trade_", "t_order_", "t_target_", "t_ledger_",
		"t_execution_", "t_rebalance_", "t_reconciliation_", "t_account_",
	} {
		if strings.HasPrefix(table, prefix) {
			return true
		}
	}
	return table == "t_accounts"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *Store) Transaction(ctx context.Context, fn func(*Tx) error) error {
	if fn == nil {
		return fmt.Errorf("%w: nil transaction callback", ErrInvalidRecord)
	}
	return s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		return fn(&Tx{db: db})
	})
}

func (s *Store) DBForTest() *gorm.DB {
	return s.db
}

func writeError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}
