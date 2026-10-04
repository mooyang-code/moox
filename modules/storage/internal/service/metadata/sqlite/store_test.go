package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataSchemaVersionIsExact(t *testing.T) {
	for _, version := range []string{"", "1", "2", "3", "4", "5", "6", "7"} {
		if version == metadataSchemaVersion {
			t.Fatalf("test case %q unexpectedly equals current schema version", version)
		}
	}
	if metadataSchemaVersion != "12" {
		t.Fatalf("metadata schema version = %q, want 12", metadataSchemaVersion)
	}
}

func TestInitSchemaAcceptsFreshDatabase(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: metadataSchemaPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("fresh database InitSchema: %v", err)
	}
	if err := store.ValidateSchemaVersion(ctx); err != nil {
		t.Fatalf("fresh database ValidateSchemaVersion: %v", err)
	}
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "12" {
		t.Fatalf("fresh database schema version = %q, want 12", version)
	}
}

func TestInitSchemaRejectsV5WithRebuildInstructions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE t_schema_meta (c_key TEXT NOT NULL PRIMARY KEY, c_value TEXT NOT NULL);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '5');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.InitSchema(ctx)
	if err == nil {
		t.Fatal("InitSchema accepted metadata schema v5")
	}
	for _, want := range []string{"schema v5", "remove", "init/import-seed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("InitSchema error %q missing %q", err, want)
		}
	}
}

func TestInitSchemaMigratesV6PeriodTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE t_schema_meta (c_key TEXT NOT NULL PRIMARY KEY, c_value TEXT NOT NULL);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '6');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema v6 migration: %v", err)
	}
	if err := store.ValidateSchemaVersion(ctx); err != nil {
		t.Fatalf("ValidateSchemaVersion after v6 migration: %v", err)
	}
	var logTable int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_view_rebuild_logs'`).Scan(&logTable); err != nil || logTable != 1 {
		t.Fatalf("v6 migration did not create rebuild log table: count=%d err=%v", logTable, err)
	}
}

func TestInitSchemaMigratesV7RebuildLogs(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE t_schema_meta (c_key TEXT NOT NULL PRIMARY KEY, c_value TEXT NOT NULL);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '7');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ValidateSchemaVersion(ctx); err != nil {
		t.Fatalf("ValidateSchemaVersion v7 migration: %v", err)
	}
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "9" {
		t.Fatalf("migrated schema version = %q", version)
	}
	var logTable int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_view_rebuild_logs'`).Scan(&logTable); err != nil || logTable != 1 {
		t.Fatalf("rebuild log table = %d err=%v", logTable, err)
	}
}

func TestInitSchemaMigratesV10SingleDatasetViews(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, v10ViewFixtureSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_views (
			c_space_id, c_view_id, c_name, c_primary_dataset_id, c_dataset_ids_json, c_attrs_json
		) VALUES ('crypto', 'view_spot', 'Spot', 'dataset_spot', '["dataset_spot"]', '{"primary_dataset_id":"dataset_spot","dataset_ids":["dataset_spot"],"attributes":{"moox.active_dataset_ids":"[\"dataset_spot\"]"}}')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name) VALUES ('crypto', 'view_spot', 'open')
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ValidateSchemaVersion(ctx); err != nil {
		t.Fatalf("ValidateSchemaVersion v10 migration: %v", err)
	}
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "12" {
		t.Fatalf("migrated schema version = %q, want 12", version)
	}
	var datasetID string
	if err := store.db.QueryRowContext(ctx, `SELECT c_dataset_id FROM t_views WHERE c_space_id = 'crypto' AND c_view_id = 'view_spot'`).Scan(&datasetID); err != nil {
		t.Fatalf("read migrated dataset_id: %v", err)
	}
	if datasetID != "dataset_spot" {
		t.Fatalf("migrated dataset_id = %q", datasetID)
	}
	var attrs string
	if err := store.db.QueryRowContext(ctx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = 'crypto' AND c_view_id = 'view_spot'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(attrs, `"dataset_id":"dataset_spot"`) {
		t.Fatalf("migrated attrs missing dataset_id: %s", attrs)
	}
	if strings.Contains(attrs, `"primary_dataset_id"`) || strings.Contains(attrs, `"dataset_ids"`) {
		t.Fatalf("migrated attrs still have v10 keys: %s", attrs)
	}
	var oldColumn int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM pragma_table_info('t_views') WHERE name IN ('c_primary_dataset_id', 'c_dataset_ids_json')`).Scan(&oldColumn); err != nil {
		t.Fatal(err)
	}
	if oldColumn != 0 {
		t.Fatalf("v10 view columns still present: %d", oldColumn)
	}
	assertNoLegacyViewParent(t, ctx, store)
	var columnCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_view_columns WHERE c_space_id = 'crypto' AND c_view_id = 'view_spot'`).Scan(&columnCount); err != nil {
		t.Fatalf("read migrated view columns: %v", err)
	}
	if columnCount != 1 {
		t.Fatalf("migrated view column count = %d, want 1", columnCount)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name) VALUES ('crypto', 'view_spot', 'close')`); err != nil {
		t.Fatalf("insert into repaired view columns: %v", err)
	}
}

func TestValidateSchemaVersionRepairsV11ViewChildForeignKeys(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		PRAGMA foreign_keys = OFF;
		CREATE TABLE t_schema_meta (c_key TEXT NOT NULL PRIMARY KEY, c_value TEXT NOT NULL);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '11');
		CREATE TABLE t_spaces (c_space_id TEXT NOT NULL PRIMARY KEY);
		INSERT INTO t_spaces (c_space_id) VALUES ('crypto');
		CREATE TABLE t_datasets (
			c_space_id TEXT NOT NULL,
			c_dataset_id TEXT NOT NULL,
			PRIMARY KEY (c_space_id, c_dataset_id)
		);
		INSERT INTO t_datasets (c_space_id, c_dataset_id) VALUES ('crypto', 'dataset_spot');
		CREATE TABLE t_views (
			c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			c_space_id TEXT NOT NULL,
			c_view_id TEXT NOT NULL,
			c_name TEXT NOT NULL,
			c_description TEXT NOT NULL DEFAULT '',
			c_dataset_id TEXT NOT NULL,
			c_grain_keys_json TEXT NOT NULL DEFAULT '[]',
			c_filter_json TEXT NOT NULL DEFAULT '{}',
			c_engine TEXT NOT NULL DEFAULT 'duckdb',
			c_keep_duration TEXT NOT NULL DEFAULT '0',
			c_active_index_id TEXT NOT NULL DEFAULT '',
			c_desired_view_revision INTEGER NOT NULL DEFAULT 1,
			c_active_view_revision INTEGER NOT NULL DEFAULT 0,
			c_active_columns_json TEXT NOT NULL DEFAULT '[]',
			c_active_view_schema_hash TEXT NOT NULL DEFAULT '',
			c_active_slot TEXT NOT NULL DEFAULT 'slot-a',
			c_indexed_from TEXT NOT NULL DEFAULT '',
			c_indexed_to TEXT NOT NULL DEFAULT '',
			c_status TEXT NOT NULL DEFAULT 'active',
			c_attrs_json TEXT NOT NULL DEFAULT '{}',
			c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE (c_space_id, c_view_id),
			UNIQUE (c_space_id, c_name)
		);
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_dataset_id) VALUES ('crypto', 'view_spot', 'Spot', 'dataset_spot');
		CREATE TABLE t_view_columns (
			c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			c_space_id TEXT NOT NULL,
			c_view_id TEXT NOT NULL,
			c_column_name TEXT NOT NULL,
			FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views_v10 (c_space_id, c_view_id)
		);
		CREATE INDEX idx_t_view_columns_view ON t_view_columns (c_space_id, c_view_id);
		INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name) VALUES ('crypto', 'view_spot', 'open');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ValidateSchemaVersion(ctx); err != nil {
		t.Fatalf("ValidateSchemaVersion v11 foreign-key repair: %v", err)
	}
	assertNoLegacyViewParent(t, ctx, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO t_view_columns (c_space_id, c_view_id, c_column_name) VALUES ('crypto', 'view_spot', 'close')`); err != nil {
		t.Fatalf("insert into repaired v11 view columns: %v", err)
	}
}

func TestValidateSchemaVersionMigratesV11WithMissingViewChildTables(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("seed current schema: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		PRAGMA foreign_keys = OFF;
		UPDATE t_schema_meta SET c_value = '11' WHERE c_key = 'schema_version';
		DROP TABLE t_view_columns;
		DROP TABLE t_view_index_builds;
		DROP TABLE t_view_rebuild_logs;
		DROP TABLE t_view_period_dataset_states;
		DROP TABLE t_view_sync_points;
		INSERT INTO t_spaces (c_space_id, c_name) VALUES ('crypto', 'Crypto');
		INSERT INTO t_datasets (
			c_space_id, c_dataset_id, c_data_source_id, c_data_node_id, c_name, c_data_kind, c_keep_duration
		) VALUES
			('crypto', 'dataset_binance_spot_symbols', 'source', 'node', 'Legacy Symbols', 'record', '0'),
			('crypto', 'dataset_spot', 'source', 'node', 'Spot', 'time_series', '0');
		INSERT INTO t_views (c_space_id, c_view_id, c_name, c_dataset_id)
		VALUES ('crypto', 'legacy_symbols', 'Legacy Symbols', 'dataset_binance_spot_symbols'),
		       ('crypto', 'spot', 'Spot', 'dataset_spot');
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema v11 migration with missing view children: %v", err)
	}
	if err := store.ValidateSchemaVersion(ctx); err != nil {
		t.Fatalf("ValidateSchemaVersion after v11 migration: %v", err)
	}
	var legacyViews int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_views WHERE c_dataset_id = 'dataset_binance_spot_symbols'`).Scan(&legacyViews); err != nil {
		t.Fatal(err)
	}
	if legacyViews != 0 {
		t.Fatalf("legacy symbol views remain after v11 migration: %d", legacyViews)
	}
	var activeViews int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_views WHERE c_dataset_id = 'dataset_spot'`).Scan(&activeViews); err != nil {
		t.Fatal(err)
	}
	if activeViews != 1 {
		t.Fatalf("active view count after v11 migration = %d, want 1", activeViews)
	}
}

func assertNoLegacyViewParent(t *testing.T, ctx context.Context, store *Store) {
	t.Helper()
	var leftover int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE sql LIKE '%t_views_v10%'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("catalog still references t_views_v10: %d", leftover)
	}
}

func TestInitSchemaRejectsV10MultiDatasetViews(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, v10ViewFixtureSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_views (
			c_space_id, c_view_id, c_name, c_primary_dataset_id, c_dataset_ids_json
		) VALUES ('crypto', 'view_join', 'Join', 'dataset_spot', '["dataset_spot","dataset_swap"]')
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Options{Path: path, SchemaPath: metadataSchemaPath()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	err = store.ValidateSchemaVersion(ctx)
	if err == nil {
		t.Fatal("ValidateSchemaVersion accepted a multi-dataset v10 view")
	}
	for _, want := range []string{"schema v10", "single dataset"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateSchemaVersion error %q missing %q", err, want)
		}
	}
}

const v10ViewFixtureSQL = `
CREATE TABLE t_schema_meta (c_key TEXT NOT NULL PRIMARY KEY, c_value TEXT NOT NULL);
INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '10');
CREATE TABLE t_spaces (c_space_id TEXT NOT NULL PRIMARY KEY);
INSERT INTO t_spaces (c_space_id) VALUES ('crypto');
CREATE TABLE t_datasets (
	c_space_id TEXT NOT NULL,
	c_dataset_id TEXT NOT NULL,
	PRIMARY KEY (c_space_id, c_dataset_id)
);
INSERT INTO t_datasets (c_space_id, c_dataset_id) VALUES ('crypto', 'dataset_spot'), ('crypto', 'dataset_swap');
CREATE TABLE t_views (
	c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
	c_space_id TEXT NOT NULL,
	c_view_id TEXT NOT NULL,
	c_name TEXT NOT NULL,
	c_description TEXT NOT NULL DEFAULT '',
	c_primary_dataset_id TEXT NOT NULL,
	c_dataset_ids_json TEXT NOT NULL DEFAULT '[]',
	c_grain_keys_json TEXT NOT NULL DEFAULT '[]',
	c_filter_json TEXT NOT NULL DEFAULT '{}',
	c_engine TEXT NOT NULL DEFAULT 'duckdb',
	c_keep_duration TEXT NOT NULL DEFAULT '0',
	c_active_index_id TEXT NOT NULL DEFAULT '',
	c_desired_view_revision INTEGER NOT NULL DEFAULT 1,
	c_active_view_revision INTEGER NOT NULL DEFAULT 0,
	c_active_columns_json TEXT NOT NULL DEFAULT '[]',
	c_active_view_schema_hash TEXT NOT NULL DEFAULT '',
	c_active_slot TEXT NOT NULL DEFAULT 'slot-a',
	c_indexed_from TEXT NOT NULL DEFAULT '',
	c_indexed_to TEXT NOT NULL DEFAULT '',
	c_status TEXT NOT NULL DEFAULT 'active',
	c_attrs_json TEXT NOT NULL DEFAULT '{}',
	c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	UNIQUE (c_space_id, c_view_id),
	UNIQUE (c_space_id, c_name)
);
CREATE TABLE t_view_columns (
	c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
	c_space_id TEXT NOT NULL,
	c_view_id TEXT NOT NULL,
	c_column_name TEXT NOT NULL,
	FOREIGN KEY (c_space_id, c_view_id) REFERENCES t_views (c_space_id, c_view_id)
);
CREATE INDEX idx_t_view_columns_view ON t_view_columns (c_space_id, c_view_id);
`

func metadataSchemaPath() string {
	return filepath.Join("..", "..", "..", "..", "schema", "metadata.sql")
}
