// Package store owns the single SQLite persistence boundary for Trade.
package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/trade/schema"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

const sqliteBusyTimeoutMS = 5000

var (
	ErrConflict           = errors.New("trade store: conflict")
	ErrInvalidRecord      = errors.New("trade store: invalid record")
	ErrIncompatibleSchema = errors.New("trade store: incompatible schema")
)

type Store struct {
	db                    *gorm.DB
	accountLocks          sync.Map
	logicalLocks          sync.Map
	executionLocks        sync.Map
	logicalMembershipLock sync.Mutex
	metrics               *report.ModuleMetrics
}

func (s *Store) SetModuleMetrics(metrics *report.ModuleMetrics) {
	if s != nil {
		s.metrics = metrics
	}
}

func (s *Store) ModuleMetrics() *report.ModuleMetrics {
	if s == nil {
		return nil
	}
	return s.metrics
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
	// Startup is one atomic cutover: even earlier schema/identity upgrades
	// must roll back if current executable target data is not recognized.
	err = db.Transaction(initializeTradeStore)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func initializeTradeStore(db *gorm.DB) error {
	if err := preflightControlModeSchema(db); err != nil {
		return err
	}
	if _, err := migrateLegacyTradeSchema(db); err != nil {
		return err
	}
	if err := migratePaperBalanceHistoryIndex(db); err != nil {
		return fmt.Errorf("migrate paper balance history index: %w", err)
	}
	if err := migrateTargetExpiryStatus(db); err != nil {
		return fmt.Errorf("migrate target expiry status: %w", err)
	}
	if err := migrateControlModeSchema(db); err != nil {
		return fmt.Errorf("migrate control mode: %w", err)
	}
	if err := validateExistingTradeSchema(db); err != nil {
		return err
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		return fmt.Errorf("apply trade schema: %w", err)
	}
	if err := migratePinnedCurrentTargets(db); err != nil {
		return fmt.Errorf("migrate pinned current targets: %w", err)
	}
	if err := initializePaperBalances(db); err != nil {
		return fmt.Errorf("initialize paper balances: %w", err)
	}
	return nil
}

func (s *Store) LockTradingAccount(tradingAccountID string) func() {
	value, _ := s.accountLocks.LoadOrStore(tradingAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// A background scan can defer a busy account without waiting behind its sync.
func (s *Store) TryLockTradingAccount(tradingAccountID string) (func(), bool) {
	value, _ := s.accountLocks.LoadOrStore(tradingAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	if !mutex.TryLock() {
		return nil, false
	}
	return mutex.Unlock, true
}

func (s *Store) LockLogicalAccount(spaceID string, logicalAccountID string) func() {
	key := spaceID + "\x00" + logicalAccountID
	value, _ := s.logicalLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (s *Store) LockLogicalAccountContext(ctx context.Context, spaceID, logicalAccountID string) (func(), error) {
	value, _ := s.logicalLocks.LoadOrStore(spaceID+"\x00"+logicalAccountID, &sync.Mutex{})
	return lockContext(ctx, value.(*sync.Mutex))
}

func (s *Store) LockLogicalAccountExecution(
	spaceID string,
	logicalAccountID string,
) func() {
	key := spaceID + "\x00" + logicalAccountID
	value, _ := s.executionLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (s *Store) LockLogicalAccountMembership() func() {
	s.logicalMembershipLock.Lock()
	return s.logicalMembershipLock.Unlock
}

// migrateLegacyTradeSchema applies additive migrations that are safe for an
// existing production database. SQLite's CREATE TABLE IF NOT EXISTS does not
// add columns to an existing table, so the owner generation column needs an
// explicit migration before schema validation runs.
func migrateLegacyTradeSchema(db *gorm.DB) (bool, error) {
	if err := validateLegacyStrategyTargetTable(db); err != nil {
		return false, err
	}
	var exists int
	if err := db.Raw(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 't_logical_accounts'
	`).Scan(&exists).Error; err != nil {
		return false, fmt.Errorf("inspect trade legacy schema: %w", err)
	}
	if exists == 0 {
		return false, nil
	}
	var columns []tableColumn
	if err := db.Raw(`PRAGMA table_info("t_logical_accounts")`).Scan(&columns).Error; err != nil {
		return false, fmt.Errorf("inspect logical account legacy columns: %w", err)
	}
	for _, column := range columns {
		if column.Name == "c_owner_claimed_at" {
			if err := ensureOwnerRebindTable(db); err != nil {
				return false, err
			}
			if err := ensureStrategyAuthorizationColumns(db); err != nil {
				return false, err
			}
			return false, nil
		}
	}
	// Historical installations before the owner-claim index was introduced
	// have the exact legacy table DDL but no explicit owner index. Recognize
	// that shape separately; the migration below recreates the index after
	// adding the generation column. Newer legacy layouts are validated by the
	// strict control-table matcher.
	knownLegacy, err := matchesLegacyLogicalAccountShape(db)
	if err != nil {
		return false, err
	}
	known := knownLegacy
	if !known {
		known, err = validateControlTable(db, "t_logical_accounts", false)
		if err != nil {
			return false, err
		}
	}
	if !known {
		return false, nil
	}
	// Keep the DDL and owner-generation initialization atomic. If the process
	// stops between these statements, the next startup must not mistake a
	// half-migrated table for a completed migration.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			ALTER TABLE t_logical_accounts
			ADD COLUMN c_owner_claimed_at INTEGER NOT NULL DEFAULT 0
		`).Error; err != nil {
			return fmt.Errorf("migrate logical account owner generation: %w", err)
		}
		// An existing owner already represents a live lifecycle. Seed it with
		// the first positive generation so Strategy can publish new events
		// immediately; unowned legacy rows remain at zero until their first claim.
		if err := tx.Exec(`
			UPDATE t_logical_accounts
			SET c_owner_claimed_at = 1
			WHERE c_owner_runner_id IS NOT NULL AND c_owner_claimed_at = 0
		`).Error; err != nil {
			return fmt.Errorf("initialize logical account owner generation: %w", err)
		}
		if err := tx.Exec(`
			CREATE UNIQUE INDEX IF NOT EXISTS ux_logical_account_owner_runner
			ON t_logical_accounts (c_space_id, c_owner_runner_id)
			WHERE c_owner_runner_id IS NOT NULL
		`).Error; err != nil {
			return fmt.Errorf("initialize logical account owner index: %w", err)
		}
		if err := ensureOwnerRebindTable(tx); err != nil {
			return err
		}
		if err := ensureStrategyAuthorizationColumns(tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func ensureStrategyAuthorizationColumns(db *gorm.DB) error {
	if err := rebuildLegacyStrategyTargetTables(db); err != nil {
		return err
	}
	columns := map[string]string{
		"c_owner_instance_id": "TEXT",
		"c_owner_session_id":  "TEXT",
		"c_auth_fence":        "TEXT NOT NULL DEFAULT ''",
	}
	if err := ensureColumns(db, "t_logical_accounts", columns); err != nil {
		return err
	}
	if err := db.Exec(`UPDATE t_logical_accounts SET c_owner_instance_id = c_owner_runner_id WHERE (c_owner_instance_id IS NULL OR c_owner_instance_id = '') AND c_owner_runner_id IS NOT NULL`).Error; err != nil {
		return fmt.Errorf("initialize logical account instance identity: %w", err)
	}
	if err := db.Exec(`UPDATE t_logical_accounts SET c_auth_fence = 'legacy-fence-' || rowid WHERE c_auth_fence = ''`).Error; err != nil {
		return fmt.Errorf("initialize logical account auth fence: %w", err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_logical_account_owner_instance ON t_logical_accounts (c_space_id, c_owner_instance_id) WHERE c_owner_instance_id IS NOT NULL`).Error; err != nil {
		return fmt.Errorf("initialize logical account instance index: %w", err)
	}
	for table, tableColumns := range map[string]map[string]string{
		"t_logical_account_targets": {
			"c_instance_id": "TEXT NOT NULL DEFAULT ''", "c_session_id": "TEXT NOT NULL DEFAULT ''", "c_strategy_id": "TEXT NOT NULL DEFAULT ''",
			"c_bar_end_time": "INTEGER NOT NULL DEFAULT 0", "c_effective_at": "INTEGER NOT NULL DEFAULT 0", "c_valid_until": "INTEGER NOT NULL DEFAULT 0",
		},
		"t_logical_account_target_receipts": {
			"c_instance_id": "TEXT NOT NULL DEFAULT ''", "c_session_id": "TEXT NOT NULL DEFAULT ''", "c_strategy_id": "TEXT NOT NULL DEFAULT ''",
			"c_bar_end_time": "INTEGER NOT NULL DEFAULT 0", "c_effective_at": "INTEGER NOT NULL DEFAULT 0", "c_valid_until": "INTEGER NOT NULL DEFAULT 0",
		},
	} {
		if err := ensureColumns(db, table, tableColumns); err != nil {
			return err
		}
	}
	var receiptTable int
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 't_logical_account_target_receipts'`).Scan(&receiptTable).Error; err != nil {
		return fmt.Errorf("inspect target receipt table: %w", err)
	}
	if receiptTable != 0 {
		if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_target_receipts_session_bar ON t_logical_account_target_receipts (c_space_id, c_logical_account_id, c_instance_id, c_session_id, c_bar_end_time) WHERE c_instance_id <> ''`).Error; err != nil {
			return fmt.Errorf("initialize target receipt session index: %w", err)
		}
	}
	return nil
}

// rebuildLegacyStrategyTargetTables upgrades the two target tables whose
// contract gained session/bar columns and table checks. SQLite cannot add a
// table-level CHECK with ALTER TABLE; rebuilding keeps existing legacy rows
// (with empty modern identity) while making the resulting schema identical to
// a fresh install, so strict startup validation remains useful.
func rebuildLegacyStrategyTargetTables(db *gorm.DB) error {
	if err := rebuildLegacyStrategyTargetTable(db); err != nil {
		return err
	}
	return rebuildLegacyTargetReceiptTable(db)
}

func rebuildLegacyStrategyTargetTable(db *gorm.DB) error {
	if err := validateLegacyStrategyTargetTable(db); err != nil {
		return err
	}
	if !tableExists(db, "t_logical_account_targets") {
		return nil
	}
	if tableHasColumn(db, "t_logical_account_targets", "c_instance_id") {
		return nil
	}
	return db.Transaction(func(db *gorm.DB) error {
		if err := db.Exec(`
CREATE TABLE t_logical_account_targets__new (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_session_id TEXT NOT NULL DEFAULT '',
    c_strategy_id TEXT NOT NULL DEFAULT '',
    c_bar_end_time INTEGER NOT NULL DEFAULT 0,
    c_effective_at INTEGER NOT NULL DEFAULT 0,
    c_valid_until INTEGER NOT NULL DEFAULT 0,
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
    CHECK ((c_instance_id = '' AND c_session_id = '') OR
           (c_instance_id <> '' AND c_session_id <> '' AND
            c_strategy_id <> '' AND c_bar_end_time > 0 AND
            c_effective_at = c_bar_end_time AND c_valid_until > c_effective_at)),
    CHECK (c_status IN ('PENDING', 'CONVERGING', 'CONVERGED', 'BLOCKED', 'EXPIRED')),
    CHECK (json_valid(c_targets_json)),
    CHECK (json_type(c_targets_json) = 'array'),
    CHECK (json_valid(c_blocked_targets_json)),
    CHECK (json_type(c_blocked_targets_json) = 'array')
)`).Error; err != nil {
			return fmt.Errorf("create migrated logical account target table: %w", err)
		}
		if err := db.Exec(`
INSERT INTO t_logical_account_targets__new
 (c_space_id, c_logical_account_id, c_target_id, c_runner_id,
  c_command_sequence, c_targets_json, c_status, c_blocked_targets_json,
  c_last_error, c_accepted_at, c_mtime)
SELECT c_space_id, c_logical_account_id, c_target_id, c_runner_id,
       c_command_sequence, c_targets_json, c_status, c_blocked_targets_json,
       c_last_error, c_accepted_at, c_mtime
FROM t_logical_account_targets`).Error; err != nil {
			return fmt.Errorf("copy logical account target rows: %w", err)
		}
		if err := db.Exec(`DROP TABLE t_logical_account_targets`).Error; err != nil {
			return fmt.Errorf("drop legacy logical account target table: %w", err)
		}
		if err := db.Exec(`ALTER TABLE t_logical_account_targets__new RENAME TO t_logical_account_targets`).Error; err != nil {
			return fmt.Errorf("rename migrated logical account target table: %w", err)
		}
		if err := db.Exec(`
CREATE INDEX IF NOT EXISTS idx_logical_account_targets_status
ON t_logical_account_targets (c_space_id, c_status, c_mtime)`).Error; err != nil {
			return fmt.Errorf("recreate logical account target index: %w", err)
		}
		return nil
	})
}

func rebuildLegacyTargetReceiptTable(db *gorm.DB) error {
	if err := validateLegacyTargetReceiptTable(db); err != nil {
		return err
	}
	if !tableExists(db, "t_logical_account_target_receipts") {
		return nil
	}
	if tableHasColumn(db, "t_logical_account_target_receipts", "c_instance_id") {
		return nil
	}
	return db.Transaction(func(db *gorm.DB) error {
		if err := db.Exec(`
CREATE TABLE t_logical_account_target_receipts__new (
    c_space_id TEXT NOT NULL,
    c_target_id TEXT NOT NULL,
    c_runner_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_command_sequence INTEGER NOT NULL,
    c_instance_id TEXT NOT NULL DEFAULT '',
    c_session_id TEXT NOT NULL DEFAULT '',
    c_strategy_id TEXT NOT NULL DEFAULT '',
    c_bar_end_time INTEGER NOT NULL DEFAULT 0,
    c_effective_at INTEGER NOT NULL DEFAULT 0,
    c_valid_until INTEGER NOT NULL DEFAULT 0,
    c_request_hash TEXT NOT NULL,
    c_signal_time INTEGER NOT NULL,
    c_weights_json TEXT NOT NULL,
    c_equity TEXT NOT NULL,
    c_equity_source_time INTEGER NOT NULL,
    c_reference_prices_json TEXT NOT NULL,
    c_quantity_targets_json TEXT NOT NULL,
    c_accepted_at INTEGER NOT NULL,
    PRIMARY KEY (c_space_id, c_target_id),
    -- The runner is part of the sequence namespace. command_sequence remains
    -- monotonic for a runner, while target_id is the replay/idempotency key
    -- for one accepted command.
    UNIQUE (c_space_id, c_logical_account_id, c_runner_id, c_command_sequence),
    FOREIGN KEY (c_space_id, c_logical_account_id)
        REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
        ON DELETE CASCADE,
    CHECK (c_command_sequence > 0),
    CHECK ((c_instance_id = '' AND c_session_id = '') OR
           (c_instance_id <> '' AND c_session_id <> '' AND
            c_strategy_id <> '' AND c_bar_end_time > 0 AND
            c_effective_at = c_bar_end_time AND c_valid_until > c_effective_at)),
    CHECK (json_valid(c_weights_json)),
    CHECK (json_type(c_weights_json) = 'array'),
    CHECK (json_valid(c_reference_prices_json)),
    CHECK (json_type(c_reference_prices_json) = 'object'),
    CHECK (json_valid(c_quantity_targets_json)),
    CHECK (json_type(c_quantity_targets_json) = 'array')
)`).Error; err != nil {
			return fmt.Errorf("create migrated target receipt table: %w", err)
		}
		if err := db.Exec(`
INSERT INTO t_logical_account_target_receipts__new
 (c_space_id, c_target_id, c_runner_id, c_logical_account_id,
  c_command_sequence, c_request_hash, c_signal_time, c_weights_json,
  c_equity, c_equity_source_time, c_reference_prices_json,
  c_quantity_targets_json, c_accepted_at)
SELECT c_space_id, c_target_id, c_runner_id, c_logical_account_id,
       c_command_sequence, c_request_hash, c_signal_time, c_weights_json,
       c_equity, c_equity_source_time, c_reference_prices_json,
       c_quantity_targets_json, c_accepted_at
FROM t_logical_account_target_receipts`).Error; err != nil {
			return fmt.Errorf("copy target receipt rows: %w", err)
		}
		if err := db.Exec(`DROP TABLE t_logical_account_target_receipts`).Error; err != nil {
			return fmt.Errorf("drop legacy target receipt table: %w", err)
		}
		if err := db.Exec(`ALTER TABLE t_logical_account_target_receipts__new RENAME TO t_logical_account_target_receipts`).Error; err != nil {
			return fmt.Errorf("rename migrated target receipt table: %w", err)
		}
		if err := db.Exec(`
CREATE INDEX IF NOT EXISTS idx_target_receipts_logical
ON t_logical_account_target_receipts (c_space_id, c_logical_account_id, c_accepted_at)`).Error; err != nil {
			return fmt.Errorf("recreate target receipt index: %w", err)
		}
		return db.Exec(`
CREATE UNIQUE INDEX IF NOT EXISTS ux_target_receipts_session_bar
ON t_logical_account_target_receipts (
    c_space_id, c_logical_account_id, c_instance_id, c_session_id, c_bar_end_time
)
WHERE c_instance_id <> ''`).Error
	})
}

func tableExists(db *gorm.DB, table string) bool {
	var count int
	return db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count).Error == nil && count > 0
}

func tableHasColumn(db *gorm.DB, table, column string) bool {
	var columns []tableColumn
	if db.Raw(`PRAGMA table_info("`+table+`")`).Scan(&columns).Error != nil {
		return false
	}
	for _, value := range columns {
		if value.Name == column {
			return true
		}
	}
	return false
}

func ensureColumns(db *gorm.DB, table string, definitions map[string]string) error {
	var exists int
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists).Error; err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	if exists == 0 {
		return nil
	}
	var columns []tableColumn
	if err := db.Raw(`PRAGMA table_info("` + table + `")`).Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect %s columns: %w", table, err)
	}
	present := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		present[column.Name] = struct{}{}
	}
	for name, definition := range definitions {
		if _, ok := present[name]; ok {
			continue
		}
		if err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition).Error; err != nil {
			return fmt.Errorf("add %s.%s: %w", table, name, err)
		}
	}
	return nil
}

func ensureOwnerRebindTable(db *gorm.DB) error {
	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS t_logical_account_owner_rebinds (
			c_space_id TEXT NOT NULL,
			c_logical_account_id TEXT NOT NULL,
			c_rebind_key TEXT NOT NULL,
			c_runner_id TEXT NOT NULL,
			c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (c_space_id, c_logical_account_id, c_rebind_key),
			FOREIGN KEY (c_space_id, c_logical_account_id)
				REFERENCES t_logical_accounts (c_space_id, c_logical_account_id)
				ON DELETE CASCADE,
			CHECK (c_rebind_key <> ''),
			CHECK (c_runner_id <> '')
		)
	`).Error; err != nil {
		return fmt.Errorf("migrate logical account owner rebinds: %w", err)
	}
	return nil
}

const legacyLogicalAccountTableSQL = `CREATE TABLE t_logical_accounts (
    c_space_id TEXT NOT NULL,
    c_logical_account_id TEXT NOT NULL,
    c_name TEXT NOT NULL,
    c_owner_runner_id TEXT,
    c_execution_mode TEXT NOT NULL,
    c_market_type TEXT NOT NULL,
    c_settlement_asset TEXT NOT NULL,
    c_automation_state TEXT NOT NULL DEFAULT 'PAUSED',
    c_pause_reason TEXT NOT NULL DEFAULT '',
    c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_logical_account_id),
    UNIQUE (c_space_id, c_name),
    CHECK (c_execution_mode IN ('PAPER', 'LIVE')),
    CHECK (c_market_type IN ('SPOT', 'SWAP')),
    CHECK (c_automation_state IN ('ACTIVE', 'PAUSED')),
    CHECK (
        (c_automation_state = 'ACTIVE' AND c_pause_reason = '')
        OR
        (c_automation_state = 'PAUSED' AND c_pause_reason <> '')
    )
)`

func validateExistingTradeSchema(db *gorm.DB) error {
	var tables []string
	if err := db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
	`).Scan(&tables).Error; err != nil {
		return fmt.Errorf("inspect trade schema: %w", err)
	}
	approved := map[string]struct{}{
		"t_trading_accounts": {}, "t_trade_instruments": {},
		"t_trade_orders": {}, "t_order_fills": {}, "t_trading_positions": {},
		"t_logical_accounts": {}, "t_logical_account_members": {},
		"t_logical_account_owner_rebinds": {},
		"t_logical_account_targets":       {}, "t_operator_actions": {},
		"t_paper_account_configs": {}, "t_account_equity_points": {},
		"t_paper_balance_projections": {}, "t_paper_asset_balances": {},
		"t_logical_account_equity_points":   {},
		"t_logical_account_target_receipts": {},
	}
	for _, table := range tables {
		if _, found := approved[table]; !found {
			return fmt.Errorf("%w: unexpected table %s", ErrIncompatibleSchema, table)
		}
	}

	reference, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open schema reference: %w", err)
	}
	if err := reference.Exec(schema.AllSQL()).Error; err != nil {
		return fmt.Errorf("apply schema reference: %w", err)
	}
	for _, table := range tables {
		if table == "t_logical_accounts" || table == "t_operator_actions" {
			if _, err := validateControlTable(db, table, true); err != nil {
				return err
			}
			continue
		}
		got, err := inspectTableShape(db, table)
		if err != nil {
			return err
		}
		want, err := inspectTableShape(reference, table)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) {
			return fmt.Errorf(
				"%w: %s does not match current columns and constraints",
				ErrIncompatibleSchema,
				table,
			)
		}
	}
	return nil
}

type tableShape struct {
	Columns     []tableColumn
	UniqueKeys  []string
	ForeignKeys []tableForeignKey
	SchemaSQL   []string
}

type tableColumn struct {
	Name       string  `gorm:"column:name"`
	Type       string  `gorm:"column:type"`
	NotNull    int     `gorm:"column:notnull"`
	DefaultSQL *string `gorm:"column:dflt_value"`
	PrimaryKey int     `gorm:"column:pk"`
}

type tableForeignKey struct {
	Table    string `gorm:"column:table"`
	From     string `gorm:"column:from"`
	To       string `gorm:"column:to"`
	OnUpdate string `gorm:"column:on_update"`
	OnDelete string `gorm:"column:on_delete"`
	Match    string `gorm:"column:match"`
}

func inspectTableShape(db *gorm.DB, table string) (tableShape, error) {
	var shape tableShape
	quoted := strings.ReplaceAll(table, `"`, `""`)
	if err := db.Raw(`PRAGMA table_info("` + quoted + `")`).Scan(&shape.Columns).Error; err != nil {
		return tableShape{}, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	var indexes []struct {
		Name   string `gorm:"column:name"`
		Unique int    `gorm:"column:unique"`
	}
	if err := db.Raw(`PRAGMA index_list("` + quoted + `")`).Scan(&indexes).Error; err != nil {
		return tableShape{}, fmt.Errorf("inspect %s indexes: %w", table, err)
	}
	for _, index := range indexes {
		if index.Unique == 0 {
			continue
		}
		indexName := strings.ReplaceAll(index.Name, `"`, `""`)
		var columns []struct {
			Sequence int    `gorm:"column:seqno"`
			Name     string `gorm:"column:name"`
		}
		if err := db.Raw(`PRAGMA index_info("` + indexName + `")`).Scan(&columns).Error; err != nil {
			return tableShape{}, fmt.Errorf("inspect %s index %s: %w", table, index.Name, err)
		}
		sort.Slice(columns, func(i, j int) bool { return columns[i].Sequence < columns[j].Sequence })
		names := make([]string, 0, len(columns))
		for _, column := range columns {
			names = append(names, column.Name)
		}
		shape.UniqueKeys = append(shape.UniqueKeys, strings.Join(names, "\x00"))
	}
	sort.Strings(shape.UniqueKeys)
	if err := db.Raw(`PRAGMA foreign_key_list("` + quoted + `")`).Scan(&shape.ForeignKeys).Error; err != nil {
		return tableShape{}, fmt.Errorf("inspect %s foreign keys: %w", table, err)
	}
	sort.Slice(shape.ForeignKeys, func(i, j int) bool {
		left := shape.ForeignKeys[i]
		right := shape.ForeignKeys[j]
		return left.Table+"\x00"+left.From+"\x00"+left.To <
			right.Table+"\x00"+right.From+"\x00"+right.To
	})
	var objects []struct {
		Type string `gorm:"column:type"`
		Name string `gorm:"column:name"`
		SQL  string `gorm:"column:sql"`
	}
	if err := db.Raw(`
		SELECT type, name, sql FROM sqlite_master
		WHERE (type = 'table' AND name = ?)
			OR (type = 'index' AND tbl_name = ? AND sql IS NOT NULL)
		ORDER BY type, name
	`, table, table).Scan(&objects).Error; err != nil {
		return tableShape{}, fmt.Errorf("inspect %s schema SQL: %w", table, err)
	}
	for _, object := range objects {
		shape.SchemaSQL = append(
			shape.SchemaSQL,
			object.Type+"\x00"+object.Name+"\x00"+normalizeSchemaSQL(object.SQL),
		)
	}
	return shape, nil
}

func normalizeSchemaSQL(value string) string {
	// SQLite emits quoted identifiers after ALTER TABLE ... RENAME. Treat
	// those quotes as formatting so an additive migration validates the same
	// schema as a fresh install without weakening column/constraint checks.
	value = strings.ReplaceAll(value, `"`, "")
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func matchesLegacyLogicalAccountShape(db *gorm.DB) (bool, error) {
	reference, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return false, fmt.Errorf("open legacy schema reference: %w", err)
	}
	sqlDB, err := reference.DB()
	if err != nil {
		return false, fmt.Errorf("open legacy schema reference connection: %w", err)
	}
	defer sqlDB.Close()
	if err := reference.Exec(legacyLogicalAccountTableSQL).Error; err != nil {
		return false, fmt.Errorf("apply legacy schema reference: %w", err)
	}
	got, err := inspectTableShape(db, "t_logical_accounts")
	if err != nil {
		return false, err
	}
	want, err := inspectTableShape(reference, "t_logical_accounts")
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(got, want), nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
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

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
