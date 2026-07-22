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

func TestMetadataSchemaV5Contract(t *testing.T) {
	sql, err := os.ReadFile("metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(sql)
	for _, want := range []string{
		"VALUES ('schema_version', '5')",
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
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metadata schema missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"t_primary_store_nodes",
		"t_primary_store_routes",
		"t_dataset_topology_locks",
		"c_" + "retention_window",
		"c_content_hash",
		"c_required",
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

func TestMetadataSchemaV5DDLExecutes(t *testing.T) {
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
	if version != "5" {
		t.Fatalf("persisted schema version = %q, want 5", version)
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
	for _, table := range []string{"t_primary_store_nodes", "t_primary_store_routes", "t_dataset_topology_locks"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("removed table %q still exists", table)
		}
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
