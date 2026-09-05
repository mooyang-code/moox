package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Open opens the Strategy SQLite database and configures its single-writer pool.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("strategy database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create strategy database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open strategy database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("open strategy sql database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	store := New(db)
	if err := store.validateExistingSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return store, nil
}

// Ping verifies that the database is available.
func (m *Store) Ping(ctx context.Context) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("strategy database is not open")
	}
	return m.db.WithContext(ctx).Exec("SELECT 1").Error
}

// ApplySchema applies the supplied schema SQL.
func (m *Store) ApplySchema(sql string) error {
	if m == nil || m.db == nil {
		return fmt.Errorf("strategy database is not open")
	}
	if strings.TrimSpace(sql) == "" {
		return fmt.Errorf("strategy schema sql is empty")
	}
	if err := m.db.Exec(sql).Error; err != nil {
		return err
	}
	return m.validateCurrentSchema()
}

var strategySchemaColumns = map[string][]string{
	"t_strategies": {
		"strategy_id", "strategy_name", "dsl_yaml", "created_at", "updated_at",
	},
	"t_strategy_instances": {
		"instance_id", "strategy_id", "space_id", "input_bindings_json",
		"logical_account_id", "enabled", "session_id", "created_at", "updated_at",
	},
	"t_strategy_results": {
		"result_id", "instance_id", "session_id", "bar_end_time", "valid_until",
		"snapshot_json", "targets_json", "rule_states_json", "event_data", "publish_status", "created_at",
	},
}

var strategySchemaRequiredColumns = map[string]map[string]bool{
	"t_strategies": {
		"strategy_id": true, "strategy_name": true, "dsl_yaml": true, "created_at": true, "updated_at": true,
	},
	"t_strategy_instances": {
		"instance_id": true, "strategy_id": true, "space_id": true, "input_bindings_json": true,
		"enabled": true, "created_at": true, "updated_at": true,
	},
	"t_strategy_results": {
		"result_id": true, "instance_id": true, "session_id": true, "bar_end_time": true,
		"valid_until": true, "snapshot_json": true, "targets_json": true,
		"rule_states_json": true, "publish_status": true, "created_at": true,
	},
}

var strategySchemaPrimaryKeys = map[string]string{
	"t_strategies":         "strategy_id",
	"t_strategy_instances": "instance_id",
	"t_strategy_results":   "result_id",
}

func (m *Store) validateExistingSchema() error {
	tables, err := m.strategyTables()
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}
	return m.validateSchemaTables(tables)
}

func (m *Store) validateCurrentSchema() error {
	tables, err := m.strategyTables()
	if err != nil {
		return err
	}
	return m.validateSchemaTables(tables)
}

func (m *Store) strategyTables() ([]string, error) {
	var tables []string
	if err := m.db.Raw(`
		SELECT name
		FROM sqlite_master
		WHERE type = 'table'
		  AND (name = 't_strategies' OR name LIKE 't_strategy_%')
		ORDER BY name
	`).Scan(&tables).Error; err != nil {
		return nil, fmt.Errorf("inspect strategy schema tables: %w", err)
	}
	return tables, nil
}

func (m *Store) validateSchemaTables(tables []string) error {
	for _, table := range tables {
		if _, ok := strategySchemaColumns[table]; !ok {
			return obsoleteSchemaError(table)
		}
	}
	if len(tables) != len(strategySchemaColumns) {
		table := "Strategy"
		if len(tables) > 0 {
			table = tables[0]
		}
		return obsoleteSchemaError(table)
	}
	for _, table := range tables {
		var columns []struct {
			Name    string `gorm:"column:name"`
			NotNull int    `gorm:"column:not_null"`
			PK      int    `gorm:"column:pk"`
		}
		if err := m.db.Raw(
			`SELECT name, "notnull" AS not_null, pk FROM pragma_table_info(?) ORDER BY cid`,
			table,
		).Scan(&columns).Error; err != nil {
			return fmt.Errorf("inspect strategy schema table %s: %w", table, err)
		}
		names := make([]string, 0, len(columns))
		validConstraints := true
		for _, column := range columns {
			names = append(names, column.Name)
			if strategySchemaRequiredColumns[table][column.Name] && column.NotNull != 1 && column.PK == 0 {
				validConstraints = false
			}
			if column.Name == strategySchemaPrimaryKeys[table] && column.PK != 1 {
				validConstraints = false
			}
		}
		if strings.Join(names, "\x00") != strings.Join(strategySchemaColumns[table], "\x00") ||
			!validConstraints {
			return obsoleteSchemaError(table)
		}
	}
	if err := m.validateInstanceOwnerIndex(); err != nil {
		return err
	}
	if err := m.validateResultIndexes(); err != nil {
		return err
	}
	return nil
}

func (m *Store) validateInstanceOwnerIndex() error {
	const indexName = "ux_strategy_instances_enabled_account"
	var index struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Partial int    `gorm:"column:partial"`
	}
	if err := m.db.Raw(`
		SELECT name, [unique], partial
		FROM pragma_index_list('t_strategy_instances')
		WHERE name = ?
	`, indexName).Scan(&index).Error; err != nil {
		return fmt.Errorf("inspect Strategy instance owner index: %w", err)
	}
	if index.Name != indexName || index.Unique != 1 || index.Partial != 1 {
		return obsoleteSchemaError("t_strategy_instances")
	}
	var columns []string
	if err := m.db.Raw(
		"SELECT name FROM pragma_index_info(?) ORDER BY seqno",
		indexName,
	).Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect Strategy instance owner index columns: %w", err)
	}
	if strings.Join(columns, "\x00") != "space_id\x00logical_account_id" {
		return obsoleteSchemaError("t_strategy_instances")
	}
	var sql string
	if err := m.db.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		indexName,
	).Scan(&sql).Error; err != nil {
		return fmt.Errorf("inspect Strategy instance owner index SQL: %w", err)
	}
	const expected = "CREATE UNIQUE INDEX ux_strategy_instances_enabled_account " +
		"ON t_strategy_instances (space_id, logical_account_id) " +
		"WHERE enabled = 1 AND logical_account_id IS NOT NULL"
	if strings.Join(strings.Fields(sql), " ") != expected {
		return obsoleteSchemaError("t_strategy_instances")
	}
	return nil
}

func (m *Store) validateResultIndexes() error {
	var indexes []struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Partial int    `gorm:"column:partial"`
	}
	if err := m.db.Raw("SELECT name, [unique], partial FROM pragma_index_list('t_strategy_results')").
		Scan(&indexes).Error; err != nil {
		return fmt.Errorf("inspect Strategy result indexes: %w", err)
	}
	const uniqueIndexName = "ux_strategy_results_session_bar"
	const pendingIndexName = "ix_strategy_results_pending"
	uniqueFound := false
	pendingFound := false
	for _, index := range indexes {
		if index.Name != uniqueIndexName && index.Name != pendingIndexName {
			continue
		}
		var columns []string
		if err := m.db.Raw(
			"SELECT name FROM pragma_index_info(?) ORDER BY seqno",
			index.Name,
		).Scan(&columns).Error; err != nil {
			return fmt.Errorf("inspect Strategy result index %s: %w", index.Name, err)
		}
		switch index.Name {
		case uniqueIndexName:
			uniqueFound = index.Unique == 1 && index.Partial == 0 && strings.Join(columns, "\x00") == "instance_id\x00session_id\x00bar_end_time"
		case pendingIndexName:
			pendingFound = index.Unique == 0 && index.Partial == 1 && strings.Join(columns, "\x00") == "created_at\x00result_id"
		}
	}
	if !uniqueFound || !pendingFound {
		return obsoleteSchemaError("t_strategy_results")
	}
	var pendingSQL string
	if err := m.db.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		pendingIndexName,
	).Scan(&pendingSQL).Error; err != nil {
		return fmt.Errorf("inspect Strategy pending index SQL: %w", err)
	}
	const expectedPending = "CREATE INDEX ix_strategy_results_pending " +
		"ON t_strategy_results (created_at, result_id) " +
		"WHERE publish_status = 'pending'"
	if strings.Join(strings.Fields(pendingSQL), " ") != expectedPending {
		return obsoleteSchemaError("t_strategy_results")
	}
	return nil
}

func obsoleteSchemaError(table string) error {
	return fmt.Errorf("Strategy 数据库表 %s 使用旧 schema；请删除旧数据库后重建", table)
}

// Close releases the underlying SQL connection.
func (m *Store) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
