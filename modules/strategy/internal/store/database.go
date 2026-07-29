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
		"strategy_id", "name", "manifest_yaml", "source_code", "source_hash", "created_at",
	},
	"t_strategy_runners": {
		"runner_id", "strategy_id", "space_id", "view_id", "frequency", "params_json",
		"logical_account_id", "status", "current_targets_json", "command_sequence",
		"last_result_id", "last_success_at", "last_error", "created_at", "updated_at",
	},
	"t_strategy_results": {
		"result_id", "runner_id", "strategy_id", "trigger_bar_time", "namespace",
		"input_hash", "action", "output_json", "command_sequence", "created_at",
	},
	"t_strategy_outbox": {
		"message_id", "event_data", "created_at",
	},
}

var strategySchemaRequiredColumns = map[string]map[string]bool{
	"t_strategies": {
		"strategy_id": true, "name": true, "manifest_yaml": true,
		"source_code": true, "source_hash": true, "created_at": true,
	},
	"t_strategy_runners": {
		"runner_id": true, "strategy_id": true, "space_id": true, "view_id": true,
		"frequency": true, "params_json": true, "status": true,
		"current_targets_json": true, "command_sequence": true,
		"created_at": true, "updated_at": true,
	},
	"t_strategy_results": {
		"result_id": true, "runner_id": true, "strategy_id": true,
		"trigger_bar_time": true, "namespace": true, "input_hash": true,
		"action": true, "output_json": true, "created_at": true,
	},
	"t_strategy_outbox": {
		"message_id": true, "event_data": true, "created_at": true,
	},
}

var strategySchemaPrimaryKeys = map[string]string{
	"t_strategies":       "strategy_id",
	"t_strategy_runners": "runner_id",
	"t_strategy_results": "result_id",
	"t_strategy_outbox":  "message_id",
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
	if err := m.validateRunnerOwnerIndex(); err != nil {
		return err
	}
	if err := m.validateResultLogicalUnique(); err != nil {
		return err
	}
	return nil
}

func (m *Store) validateRunnerOwnerIndex() error {
	const indexName = "ux_strategy_runners_enabled_logical_account"
	var index struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Partial int    `gorm:"column:partial"`
	}
	if err := m.db.Raw(`
		SELECT name, [unique], partial
		FROM pragma_index_list('t_strategy_runners')
		WHERE name = ?
	`, indexName).Scan(&index).Error; err != nil {
		return fmt.Errorf("inspect Strategy runner owner index: %w", err)
	}
	if index.Name != indexName || index.Unique != 1 || index.Partial != 1 {
		return obsoleteSchemaError("t_strategy_runners")
	}
	var columns []string
	if err := m.db.Raw(
		"SELECT name FROM pragma_index_info(?) ORDER BY seqno",
		indexName,
	).Scan(&columns).Error; err != nil {
		return fmt.Errorf("inspect Strategy runner owner index columns: %w", err)
	}
	if strings.Join(columns, "\x00") != "space_id\x00logical_account_id" {
		return obsoleteSchemaError("t_strategy_runners")
	}
	var sql string
	if err := m.db.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?",
		indexName,
	).Scan(&sql).Error; err != nil {
		return fmt.Errorf("inspect Strategy runner owner index SQL: %w", err)
	}
	const expected = "CREATE UNIQUE INDEX ux_strategy_runners_enabled_logical_account " +
		"ON t_strategy_runners (space_id, logical_account_id) " +
		"WHERE logical_account_id IS NOT NULL AND status = 'ENABLED'"
	if strings.Join(strings.Fields(sql), " ") != expected {
		return obsoleteSchemaError("t_strategy_runners")
	}
	return nil
}

func (m *Store) validateResultLogicalUnique() error {
	var indexes []struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Partial int    `gorm:"column:partial"`
	}
	if err := m.db.Raw("SELECT name, [unique], partial FROM pragma_index_list('t_strategy_results')").
		Scan(&indexes).Error; err != nil {
		return fmt.Errorf("inspect Strategy result indexes: %w", err)
	}
	want := "runner_id\x00strategy_id\x00namespace\x00trigger_bar_time"
	for _, index := range indexes {
		if index.Unique != 1 || index.Partial != 0 {
			continue
		}
		var columns []string
		if err := m.db.Raw(
			"SELECT name FROM pragma_index_info(?) ORDER BY seqno",
			index.Name,
		).Scan(&columns).Error; err != nil {
			return fmt.Errorf("inspect Strategy result index %s: %w", index.Name, err)
		}
		if strings.Join(columns, "\x00") == want {
			return nil
		}
	}
	return obsoleteSchemaError("t_strategy_results")
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
