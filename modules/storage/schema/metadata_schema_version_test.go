package schema

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMetadataSchemaV10Contract(t *testing.T) {
	sql, err := os.ReadFile("metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(sql)
	for _, want := range []string{
		"VALUES ('schema_version', '10')",
		"CREATE TABLE IF NOT EXISTS t_data_nodes",
		"c_node_id TEXT NOT NULL",
		"c_name TEXT NOT NULL",
		"c_service_target TEXT NOT NULL",
		"c_status TEXT NOT NULL DEFAULT 'active' CHECK (c_status IN ('active', 'disabled'))",
		"c_data_node_id TEXT NOT NULL,",
		"c_keep_duration TEXT NOT NULL,",
		"c_binding_locked INTEGER NOT NULL DEFAULT 0 CHECK (c_binding_locked IN (0, 1))",
		"c_revision INTEGER NOT NULL DEFAULT 1 CHECK (c_revision > 0)",
		"c_status TEXT NOT NULL DEFAULT 'disabled' CHECK (c_status IN ('active', 'disabled'))",
		"FOREIGN KEY (c_data_node_id) REFERENCES t_data_nodes (c_node_id) ON DELETE RESTRICT",
		"CREATE INDEX IF NOT EXISTS idx_t_datasets_data_node_id ON t_datasets (c_data_node_id)",
		"c_active_slot",
		"c_new_slot",
		"c_backfilled_rows",
		"c_safe_error",
		"CREATE TABLE IF NOT EXISTS t_view_period_dataset_states",
		"CREATE TABLE IF NOT EXISTS t_view_sync_points",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metadata schema missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"t_" + "primary" + "_" + "store" + "_" + "nodes",
		"t_" + "primary" + "_" + "store" + "_" + "routes",
		"t_dataset_" + "topology" + "_locks",
		"c_" + "retention_window",
		"c_content_hash",
		"c_required",
		"ALTER TABLE",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("metadata schema contains removed schema element %q", forbidden)
		}
	}
	nodeStart := strings.Index(text, "CREATE TABLE IF NOT EXISTS t_data_nodes")
	datasetStart := strings.Index(text, "CREATE TABLE IF NOT EXISTS t_datasets")
	if nodeStart < 0 || datasetStart < 0 || nodeStart > datasetStart {
		t.Fatal("t_data_nodes must be created before t_datasets")
	}

	deviceStart := strings.Index(text, "CREATE TABLE IF NOT EXISTS t_storage_devices")
	if deviceStart < 0 {
		t.Fatal("metadata schema missing t_storage_devices")
	}
	deviceEnd := strings.Index(text[deviceStart:], ");")
	if deviceEnd < 0 {
		t.Fatal("metadata schema has unterminated t_storage_devices")
	}
	if strings.Contains(text[deviceStart:deviceStart+deviceEnd], "c_node_id") {
		t.Error("t_storage_devices still contains the removed c_node_id relation")
	}
}

func TestMetadataSchemaV10DDLExecutes(t *testing.T) {
	schema, err := os.ReadFile("metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "metadata.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		t.Fatalf("execute metadata schema: %v", err)
	}
	var version string
	if err := db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "10" {
		t.Fatalf("persisted schema version = %q, want 10", version)
	}
	var foreignKeysEnabled int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeysEnabled); err != nil {
		t.Fatal(err)
	}
	if foreignKeysEnabled != 1 {
		t.Fatal("metadata schema did not enable foreign keys")
	}

	for _, table := range []string{"t_data_nodes", "t_datasets", "t_storage_devices"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %q count = %d, want 1", table, count)
		}
	}
	for _, table := range []string{
		"t_" + "primary" + "_" + "store" + "_" + "nodes",
		"t_" + "primary" + "_" + "store" + "_" + "routes",
		"t_dataset_" + "topology" + "_locks",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("removed table %q still exists", table)
		}
	}

	dataNodeColumns := pragmaTableInfo(t, db, "t_data_nodes")
	for _, want := range []struct {
		name     string
		typeName string
	}{
		{name: "c_node_id", typeName: "TEXT"},
		{name: "c_name", typeName: "TEXT"},
		{name: "c_service_target", typeName: "TEXT"},
		{name: "c_status", typeName: "TEXT"},
		{name: "c_ctime", typeName: "DATETIME"},
		{name: "c_mtime", typeName: "DATETIME"},
	} {
		column := requireColumn(t, dataNodeColumns, want.name, want.typeName)
		if column.notNull != 1 {
			t.Errorf("t_data_nodes.%s notnull = %d, want 1", want.name, column.notNull)
		}
	}
	assertColumnDefault(t, dataNodeColumns, "c_status", "'active'")

	datasetColumns := pragmaTableInfo(t, db, "t_datasets")
	for _, want := range []struct {
		name     string
		typeName string
	}{
		{name: "c_data_node_id", typeName: "TEXT"},
		{name: "c_keep_duration", typeName: "TEXT"},
		{name: "c_binding_locked", typeName: "INTEGER"},
		{name: "c_revision", typeName: "INTEGER"},
		{name: "c_status", typeName: "TEXT"},
	} {
		column := requireColumn(t, datasetColumns, want.name, want.typeName)
		if column.notNull != 1 {
			t.Errorf("t_datasets.%s notnull = %d, want 1", want.name, column.notNull)
		}
	}
	assertColumnDefault(t, datasetColumns, "c_binding_locked", "0")
	assertColumnDefault(t, datasetColumns, "c_revision", "1")
	assertColumnDefault(t, datasetColumns, "c_status", "'disabled'")
	if column := requireColumn(t, datasetColumns, "c_data_node_id", "TEXT"); column.defaultValue.Valid {
		t.Fatalf("t_datasets.c_data_node_id default = %q, want none", column.defaultValue.String)
	}

	assertDatasetDataNodeForeignKey(t, db)
	assertDatasetDataNodeIndex(t, db)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_spaces (c_space_id, c_name) VALUES ('space', 'Space');
		INSERT INTO t_data_sources (c_space_id, c_data_source_id, c_name, c_kind)
		VALUES ('space', 'source', 'Source', 'internal');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_datasets (c_space_id, c_dataset_id, c_data_source_id, c_data_node_id, c_name, c_data_kind, c_keep_duration)
		VALUES ('space', 'unknown-node', 'source', 'missing', 'Unknown Node', 'record', '0');
	`); err == nil {
		t.Fatal("dataset accepted an unknown data node")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_data_nodes (c_node_id, c_name, c_service_target)
		VALUES ('node-1', 'Node 1', 'trpc://node-1');
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_datasets (c_space_id, c_dataset_id, c_data_source_id, c_data_node_id, c_name, c_data_kind, c_keep_duration)
		VALUES ('space', 'registered-node', 'source', 'node-1', 'Registered Node', 'record', '0');
	`); err != nil {
		t.Fatalf("dataset rejected a registered data node: %v", err)
	}

	rows, err := db.QueryContext(ctx, `PRAGMA table_info('t_storage_devices')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "c_node_id" {
			t.Fatal("t_storage_devices contains removed c_node_id")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

type metadataColumn struct {
	typeName     string
	notNull      int
	defaultValue sql.NullString
}

func pragmaTableInfo(t *testing.T, db *sql.DB, table string) map[string]metadataColumn {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info('` + table + `')`)
	if err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]metadataColumn)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		columns[name] = metadataColumn{typeName: columnType, notNull: notNull, defaultValue: defaultValue}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func requireColumn(t *testing.T, columns map[string]metadataColumn, name, typeName string) metadataColumn {
	t.Helper()
	column, ok := columns[name]
	if !ok {
		t.Fatalf("missing column %q", name)
	}
	if typeName != "" && column.typeName != typeName {
		t.Fatalf("column %s type = %q, want %q", name, column.typeName, typeName)
	}
	return column
}

func assertColumnDefault(t *testing.T, columns map[string]metadataColumn, name, want string) {
	t.Helper()
	column := requireColumn(t, columns, name, "")
	if !column.defaultValue.Valid || column.defaultValue.String != want {
		t.Errorf("column %s default = %q (valid=%t), want %q", name, column.defaultValue.String, column.defaultValue.Valid, want)
	}
}

func assertDatasetDataNodeForeignKey(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA foreign_key_list('t_datasets')`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var id, seq int
		var tableName, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &tableName, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if tableName == "t_data_nodes" && from == "c_data_node_id" && to == "c_node_id" {
			found = true
			if onDelete != "RESTRICT" {
				t.Errorf("DataNode FK on_delete = %q, want RESTRICT", onDelete)
			}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("t_datasets is missing the DataNode foreign key")
	}
}

func assertDatasetDataNodeIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA index_list('t_datasets')`)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if name == "idx_t_datasets_data_node_id" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("t_datasets is missing idx_t_datasets_data_node_id")
	}

	rows, err = db.Query(`PRAGMA index_info('idx_t_datasets_data_node_id')`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var seqNo, columnID int
		var columnName string
		if err := rows.Scan(&seqNo, &columnID, &columnName); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		columns = append(columns, columnName)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(columns) != 1 || columns[0] != "c_data_node_id" {
		t.Fatalf("dataset DataNode index columns = %v, want [c_data_node_id]", columns)
	}
}
