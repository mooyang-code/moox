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
	_, err = migrateLegacyTradeSchema(db)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
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

func (s *Store) LockTradingAccount(tradingAccountID string) func() {
	value, _ := s.accountLocks.LoadOrStore(tradingAccountID, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (s *Store) LockLogicalAccount(spaceID string, logicalAccountID string) func() {
	key := spaceID + "\x00" + logicalAccountID
	value, _ := s.logicalLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
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
			return false, nil
		}
	}
	var tableSQL string
	if err := db.Raw(`
		SELECT sql FROM sqlite_master
		WHERE type = 'table' AND name = 't_logical_accounts'
	`).Scan(&tableSQL).Error; err != nil {
		return false, fmt.Errorf("inspect logical account legacy SQL: %w", err)
	}
	if normalizeSchemaSQL(tableSQL) != normalizeSchemaSQL(legacyLogicalAccountTableSQL) {
		// Unknown shapes remain fail-closed and are reported by the strict
		// validator below rather than being partially mutated.
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
		if err := ensureOwnerRebindTable(tx); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
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
		got, err := inspectTableShape(db, table)
		if err != nil {
			return err
		}
		want, err := inspectTableShape(reference, table)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) && !(table == "t_logical_accounts" && legacyLogicalAccountShapeMatches(got, want)) {
			return fmt.Errorf(
				"%w: %s does not match current columns and constraints",
				ErrIncompatibleSchema,
				table,
			)
		}
	}
	return nil
}

func legacyLogicalAccountShapeMatches(got, want tableShape) bool {
	if !sameColumnsIgnoringOwnerGenerationPosition(got.Columns, want.Columns) ||
		!reflect.DeepEqual(got.UniqueKeys, want.UniqueKeys) ||
		!reflect.DeepEqual(got.ForeignKeys, want.ForeignKeys) ||
		len(got.SchemaSQL) != len(want.SchemaSQL) {
		return false
	}
	for index := range got.SchemaSQL {
		gotTable := strings.HasPrefix(got.SchemaSQL[index], "table\x00t_logical_accounts\x00")
		wantTable := strings.HasPrefix(want.SchemaSQL[index], "table\x00t_logical_accounts\x00")
		if gotTable || wantTable {
			if !gotTable || !wantTable || !strings.Contains(want.SchemaSQL[index], "c_owner_claimed_at") || !strings.HasSuffix(got.SchemaSQL[index], normalizeSchemaSQL(legacyLogicalAccountMigratedTableSQL)) {
				return false
			}
			continue
		}
		if got.SchemaSQL[index] != want.SchemaSQL[index] {
			return false
		}
	}
	return true
}

var legacyLogicalAccountMigratedTableSQL = strings.Replace(
	legacyLogicalAccountTableSQL,
	"    PRIMARY KEY (c_space_id, c_logical_account_id),",
	"    c_owner_claimed_at INTEGER NOT NULL DEFAULT 0,\n    PRIMARY KEY (c_space_id, c_logical_account_id),",
	1,
)

func sameColumnsIgnoringOwnerGenerationPosition(got, want []tableColumn) bool {
	if len(got) != len(want) {
		return false
	}
	byName := make(map[string]tableColumn, len(got))
	for _, column := range got {
		byName[column.Name] = column
	}
	for _, column := range want {
		actual, ok := byName[column.Name]
		if !ok || actual.Type != column.Type || actual.NotNull != column.NotNull || actual.PrimaryKey != column.PrimaryKey {
			return false
		}
		if (actual.DefaultSQL == nil) != (column.DefaultSQL == nil) {
			return false
		}
		if actual.DefaultSQL != nil && *actual.DefaultSQL != *column.DefaultSQL {
			return false
		}
	}
	return true
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
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
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
