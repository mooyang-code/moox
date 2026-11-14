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

func TestMetadataSchemaContract(t *testing.T) {
	sql, err := os.ReadFile("metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(sql)
	for _, want := range []string{
		"VALUES ('schema_version', '18')",
		"CREATE TABLE IF NOT EXISTS t_tags (",
		"CREATE TABLE IF NOT EXISTS t_subject_tags (",
		"c_subject_tags_json TEXT NOT NULL DEFAULT '[]'",
		"CHECK (c_mode IN ('auto', 'manual'))",
		"CHECK (c_status IN ('active', 'inactive'))",
		"CREATE TRIGGER IF NOT EXISTS trg_t_tags_mtime",
		"CREATE TRIGGER IF NOT EXISTS trg_t_subject_tags_mtime",
		"c_name TEXT NOT NULL",
		"c_freq TEXT NOT NULL DEFAULT '',",
		"c_revision INTEGER NOT NULL DEFAULT 1 CHECK (c_revision > 0)",
		"c_status TEXT NOT NULL DEFAULT 'active' CHECK (c_status IN ('active', 'disabled'))",
		"c_dataset_id TEXT NOT NULL",
		"CREATE INDEX IF NOT EXISTS idx_t_views_dataset ON t_views (c_space_id, c_dataset_id, c_status)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metadata schema missing %q", want)
		}
	}
	removedTables := []string{
		"t_" + "subject" + "_symbols",
		"t_" + "dataset" + "_subjects",
		"t_" + "dataset_subject_set_staging",
	}
	for _, forbidden := range append([]string{
		"t_" + "primary" + "_" + "store" + "_" + "nodes",
		"t_" + "primary" + "_" + "store" + "_" + "routes",
		"t_dataset_" + "topology" + "_locks",
		"c_" + "retention_window",
		"c_content_hash",
		"c_required",
		"ALTER TABLE",
		"t_subject_symbols",
		"t_dataset_subjects",
		"t_dataset_subject_set_staging",
		"c_primary_dataset_id",
		"c_dataset_ids_json",
		"c_active_slot",
		"c_desired_view_revision",
		"t_view_index_builds",
		"t_view_rebuild_logs",
		"t_data_nodes",
		"t_view_period_dataset_states",
		"t_view_sync_points",
		"t_view_columns",
		"c_active_index_id",
		"c_view_revision",
		"c_active_columns_json",
		"c_active_view_schema_hash",
		"c_indexed_from",
		"c_indexed_to",
		"t_storage_devices",
		"c_attrs_json",
		"'building'",
		"'archived'",
	}, removedTables...) {
		if strings.Contains(text, forbidden) {
			t.Errorf("metadata schema contains removed schema element %q", forbidden)
		}
	}
}

func TestMetadataSchemaDDLExecutes(t *testing.T) {
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
	if version != "18" {
		t.Fatalf("persisted schema version = %q, want 18", version)
	}
	var foreignKeysEnabled int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeysEnabled); err != nil {
		t.Fatal(err)
	}
	if foreignKeysEnabled != 1 {
		t.Fatal("metadata schema did not enable foreign keys")
	}

	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name LIKE 't\_%' ESCAPE '\'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 12 {
		t.Fatalf("metadata tables = %d, want 12", tables)
	}
	for _, table := range []string{
		"t_" + "primary" + "_" + "store" + "_" + "nodes",
		"t_" + "primary" + "_" + "store" + "_" + "routes",
		"t_dataset_" + "topology" + "_locks",
		"t_data_nodes",
		"t_storage_devices",
		"t_view_columns",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("removed table %q still exists", table)
		}
	}

	datasetColumns := pragmaTableInfo(t, db, "t_datasets")
	for _, want := range []struct {
		name     string
		typeName string
	}{
		{name: "c_freq", typeName: "TEXT"},
		{name: "c_revision", typeName: "INTEGER"},
		{name: "c_status", typeName: "TEXT"},
	} {
		column := requireColumn(t, datasetColumns, want.name, want.typeName)
		if column.notNull != 1 {
			t.Errorf("t_datasets.%s notnull = %d, want 1", want.name, column.notNull)
		}
	}
	assertColumnDefault(t, datasetColumns, "c_revision", "1")
	assertColumnDefault(t, datasetColumns, "c_status", "'active'")

	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_spaces (c_space_id, c_name) VALUES ('space', 'Space');
		INSERT INTO t_data_sources (c_space_id, c_data_source_id, c_name)
		VALUES ('space', 'source', 'Source');
		INSERT INTO t_datasets (c_space_id, c_dataset_id, c_data_source_id, c_name, c_data_kind)
		VALUES ('space', 'dataset', 'source', 'Dataset', 'record');
	`); err != nil {
		t.Fatalf("insert dataset: %v", err)
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
