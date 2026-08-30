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
	if err := store.migrateLegacySchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := store.migrateLegacyOwnerMarkers(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := store.validateExistingSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return store, nil
}

// migrateLegacyOwnerMarkers adds a small reconciliation marker to archived V1
// runner tables. The marker is outside the active V2 schema and prevents a
// successful owner release from generating the same idempotent RPC forever.
func (m *Store) migrateLegacyOwnerMarkers() error {
	var tables []string
	if err := m.db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name LIKE 'legacy_strategy_v1_strategy_runners%'
		ORDER BY name
	`).Scan(&tables).Error; err != nil {
		return fmt.Errorf("inspect archived Strategy runners: %w", err)
	}
	for _, table := range tables {
		columns, err := strategyTableColumns(m.db, table)
		if err != nil {
			return err
		}
		if _, ok := columns[legacyOwnerReconciledColumn]; ok {
			continue
		}
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		if err := m.db.Exec(`ALTER TABLE ` + quoted + ` ADD COLUMN ` + legacyOwnerReconciledColumn + ` INTEGER NOT NULL DEFAULT 0`).Error; err != nil {
			return fmt.Errorf("add owner reconciliation marker to %s: %w", table, err)
		}
	}
	return nil
}

// migrateLegacySchema archives the pre-V2 Strategy tables before strict
// validation. V1 stored executable Python/source metadata that cannot be
// interpreted by the V2 compiler, so retaining those rows under an archive
// namespace is safer than silently mapping them to a different meaning or
// requiring RESET_DATA during a normal deployment.
func (m *Store) migrateLegacySchema() error {
	legacy, err := m.legacySchemaDetected()
	if err != nil || !legacy {
		return err
	}
	return m.db.Transaction(func(tx *gorm.DB) error {
		for _, table := range []string{
			"t_strategies", "t_strategy_runners", "t_strategy_results",
			"t_strategy_outbox", "t_strategy_inbox",
		} {
			exists, err := tableExists(tx, table)
			if err != nil {
				return err
			}
			if !exists {
				continue
			}
			// Explicit indexes keep their names after a table rename. Drop them
			// before archiving so the V2 schema can recreate the same names.
			if err := dropTableIndexes(tx, table); err != nil {
				return err
			}
			archive, err := nextLegacyTableName(tx, "legacy_strategy_v1_"+strings.TrimPrefix(table, "t_"))
			if err != nil {
				return err
			}
			if err := tx.Exec(`ALTER TABLE "` + table + `" RENAME TO "` + archive + `"`).Error; err != nil {
				return fmt.Errorf("archive legacy Strategy table %s: %w", table, err)
			}
		}
		return nil
	})
}

func (m *Store) legacySchemaDetected() (bool, error) {
	var tables []string
	if err := m.db.Raw(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND (name = 't_strategies' OR name LIKE 't_strategy_%')
		ORDER BY name
	`).Scan(&tables).Error; err != nil {
		return false, fmt.Errorf("inspect legacy Strategy tables: %w", err)
	}
	legacyFound := false
	currentOrUnknownFound := false
	for _, table := range tables {
		columns, err := strategyTableColumns(m.db, table)
		if err != nil {
			return false, err
		}
		legacy, current := classifyStrategySchema(table, columns)
		legacyFound = legacyFound || legacy
		if current || (!legacy && table != "t_strategy_outbox") {
			currentOrUnknownFound = true
		}
	}
	if legacyFound && currentOrUnknownFound {
		return false, fmt.Errorf("Strategy 数据库同时包含 V1 与 V2/未知表，无法自动迁移；请人工处理")
	}
	if legacyFound {
		for _, table := range []string{"t_strategies", "t_strategy_runners", "t_strategy_results", "t_strategy_outbox"} {
			columns, err := strategyTableColumns(m.db, table)
			if err != nil {
				return false, err
			}
			if !hasExactStrategyColumns(table, columns, strategyV1SchemaColumns[table]...) {
				return false, fmt.Errorf("Strategy V1 表 %s 结构不完整，无法自动归档；请人工处理", table)
			}
		}
	}
	return legacyFound, nil
}

func strategyTableColumns(db *gorm.DB, table string) (map[string]struct{}, error) {
	var columns []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(`SELECT name FROM pragma_table_info(?)`, table).Scan(&columns).Error; err != nil {
		return nil, fmt.Errorf("inspect Strategy table %s: %w", table, err)
	}
	found := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		found[column.Name] = struct{}{}
	}
	return found, nil
}

func classifyStrategySchema(table string, columns map[string]struct{}) (legacy, current bool) {
	has := func(names ...string) bool {
		for _, name := range names {
			if _, ok := columns[name]; !ok {
				return false
			}
		}
		return true
	}
	switch table {
	case "t_strategies":
		return has("source_code"), has("kind", "compiled_json")
	case "t_strategy_runners":
		return has("view_id", "params_json"), has("source_view_id")
	case "t_strategy_results":
		return has("trigger_bar_time", "namespace", "output_json"), has("period_time", "targets_json")
	case "t_strategy_outbox":
		// V1 and V2 use the same outbox shape; its ownership follows the
		// surrounding strategy tables during a legacy archive migration.
		return false, !hasExactStrategyColumns(table, columns, strategyV1SchemaColumns[table]...)
	case "t_strategy_inbox":
		return false, true
	default:
		return false, false
	}
}

func hasExactStrategyColumns(table string, columns map[string]struct{}, expected ...string) bool {
	if len(columns) != len(expected) {
		return false
	}
	for _, name := range expected {
		if _, ok := columns[name]; !ok {
			return false
		}
	}
	return true
}

var strategyV1SchemaColumns = map[string][]string{
	"t_strategies": {
		"strategy_id", "name", "manifest_yaml", "source_code", "source_hash", "created_at",
	},
	"t_strategy_runners": {
		"runner_id", "strategy_id", "space_id", "view_id", "frequency", "params_json",
		"logical_account_id", "status", "current_targets_json", "command_sequence", "last_result_id",
		"last_success_at", "last_error", "created_at", "updated_at",
	},
	"t_strategy_results": {
		"result_id", "runner_id", "strategy_id", "trigger_bar_time", "namespace", "input_hash",
		"action", "output_json", "command_sequence", "created_at",
	},
	"t_strategy_outbox": {
		"message_id", "event_data", "created_at",
	},
}

func tableExists(db *gorm.DB, table string) (bool, error) {
	var count int64
	if err := db.Raw(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count).Error; err != nil {
		return false, fmt.Errorf("inspect Strategy table %s: %w", table, err)
	}
	return count > 0, nil
}

func dropTableIndexes(db *gorm.DB, table string) error {
	var indexes []struct {
		Name string `gorm:"column:name"`
	}
	if err := db.Raw(`SELECT name FROM pragma_index_list(?)`, table).Scan(&indexes).Error; err != nil {
		return fmt.Errorf("inspect legacy Strategy table %s indexes: %w", table, err)
	}
	for _, index := range indexes {
		if strings.HasPrefix(index.Name, "sqlite_autoindex_") {
			continue
		}
		if err := db.Exec(`DROP INDEX "` + strings.ReplaceAll(index.Name, `"`, `""`) + `"`).Error; err != nil {
			return fmt.Errorf("drop legacy Strategy index %s: %w", index.Name, err)
		}
	}
	return nil
}

func nextLegacyTableName(db *gorm.DB, base string) (string, error) {
	for suffix := 0; ; suffix++ {
		name := base
		if suffix > 0 {
			name = fmt.Sprintf("%s_%d", base, suffix)
		}
		exists, err := tableExists(db, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
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
		"strategy_id", "name", "kind", "manifest_yaml", "compiled_json", "source_hash", "created_at",
	},
	"t_strategy_runners": {
		"runner_id", "strategy_id", "space_id", "source_view_id", "frequency",
		"logical_account_id", "status", "current_targets_json", "command_sequence",
		"last_result_id", "last_success_at", "last_error", "created_at", "updated_at",
	},
	"t_strategy_results": {
		"result_id", "runner_id", "strategy_id", "period_time", "targets_json", "debug_info_json",
		"input_hash", "action", "command_sequence", "created_at",
	},
	"t_strategy_outbox": {
		"message_id", "event_data", "created_at",
	},
	"t_strategy_inbox": {
		"message_id", "event_name", "received_at",
	},
}

var strategySchemaRequiredColumns = map[string]map[string]bool{
	"t_strategies": {
		"strategy_id": true, "name": true, "kind": true, "manifest_yaml": true, "compiled_json": true,
		"source_hash": true, "created_at": true,
	},
	"t_strategy_runners": {
		"runner_id": true, "strategy_id": true, "space_id": true, "source_view_id": true,
		"frequency": true, "status": true,
		"current_targets_json": true, "command_sequence": true,
		"created_at": true, "updated_at": true,
	},
	"t_strategy_results": {
		"result_id": true, "runner_id": true, "strategy_id": true,
		"period_time": true, "targets_json": true, "debug_info_json": true, "input_hash": true,
		"action": true, "created_at": true,
	},
	"t_strategy_outbox": {
		"message_id": true, "event_data": true, "created_at": true,
	},
	"t_strategy_inbox": {
		"message_id": true, "event_name": true, "received_at": true,
	},
}

var strategySchemaPrimaryKeys = map[string]string{
	"t_strategies":       "strategy_id",
	"t_strategy_runners": "runner_id",
	"t_strategy_results": "result_id",
	"t_strategy_outbox":  "message_id",
	"t_strategy_inbox":   "message_id",
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
	if err := m.validateResultLogicalIndex(); err != nil {
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

func (m *Store) validateResultLogicalIndex() error {
	var indexes []struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:unique"`
		Partial int    `gorm:"column:partial"`
	}
	if err := m.db.Raw("SELECT name, [unique], partial FROM pragma_index_list('t_strategy_results')").
		Scan(&indexes).Error; err != nil {
		return fmt.Errorf("inspect Strategy result indexes: %w", err)
	}
	const indexName = "ix_strategy_results_logical_period"
	for _, index := range indexes {
		if index.Name != indexName || index.Unique != 0 || index.Partial != 0 {
			continue
		}
		var columns []string
		if err := m.db.Raw(
			"SELECT name FROM pragma_index_info(?) ORDER BY seqno",
			index.Name,
		).Scan(&columns).Error; err != nil {
			return fmt.Errorf("inspect Strategy result index %s: %w", index.Name, err)
		}
		if strings.Join(columns, "\x00") == "runner_id\x00strategy_id\x00period_time\x00created_at" {
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
