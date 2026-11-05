// Package store owns the single SQLite persistence boundary for Trade.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/cache"
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

	// 很少变化、写入点明确的数据缓存在内存里：标的定义按场所缓存整份，账户身份按账户缓存。
	// 标的和账户的写入都会清掉对应缓存，下单时不必每笔都查库。
	instrumentSets cache.Memo[instrumentScope, *instrumentSet]
	identities     cache.Memo[accountKey, tradingAccountIdentity]
}

// InvalidateCaches 清空内存缓存，用于绕过存储层直接改表之后（例如系统重置清表）。
func (s *Store) InvalidateCaches() {
	if s == nil {
		return
	}
	s.instrumentSets.Invalidate()
	s.identities.Invalidate()
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
	db    *gorm.DB
	store *Store
	// 本事务写过标的或账户时，读取直接查库：未提交的数据不能进缓存。
	instrumentsWritten bool
	accountsWritten    bool
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
	err = db.Transaction(initializeTradeStore)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func initializeTradeStore(db *gorm.DB) error {
	if err := validateExistingTradeSchema(db); err != nil {
		return err
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		return fmt.Errorf("apply trade schema: %w", err)
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
		"t_trading_position_tombstones": {},
		"t_logical_accounts":            {}, "t_logical_account_members": {},
		"t_logical_account_targets": {}, "t_operator_actions": {},
		"t_paper_account_configs": {}, "t_account_equity_points": {},
		"t_paper_balance_projections": {}, "t_paper_asset_balances": {}, "t_shadow_targets": {}, "t_replay_sessions": {}, "t_replay_periods": {}, "t_replay_orders": {},
		"t_logical_account_equity_points":   {},
		"t_logical_account_target_receipts": {},
		"t_trade_wallet_transfers":          {},
		"t_trade_blocked_instruments":       {},
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
	// Quoting and whitespace are formatting, not schema.
	value = strings.ReplaceAll(value, `"`, "")
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
	var written *Tx
	err := s.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		tx := &Tx{db: db, store: s}
		written = tx
		return fn(tx)
	})
	// 提交或回滚之后再清一次：事务内写入时已经清过，这里覆盖事务期间别处重新填进去的旧数据。
	if written != nil {
		if written.instrumentsWritten {
			s.instrumentSets.Invalidate()
		}
		if written.accountsWritten {
			s.identities.Invalidate()
		}
	}
	return err
}

func (s *Store) DBForTest() *gorm.DB {
	return s.db
}

// SQLDB 返回底层数据库连接（重置时备份与清表用，由生命周期的重置实现调用）。
func (s *Store) SQLDB() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("trade store is not open")
	}
	return s.db.DB()
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
