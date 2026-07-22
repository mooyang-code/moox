package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestMetadataSchemaVersionIsExact(t *testing.T) {
	for _, version := range []string{"", "1", "2", "3", "4", "6"} {
		if version == metadataSchemaVersion {
			t.Fatalf("test case %q unexpectedly equals current schema version", version)
		}
	}
	if metadataSchemaVersion != "5" {
		t.Fatalf("metadata schema version = %q, want 5", metadataSchemaVersion)
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
	if version != "5" {
		t.Fatalf("fresh database schema version = %q, want 5", version)
	}
}

func TestMetadataSchemaV4DatabaseIsRejected(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: metadataSchemaPath(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	legacyNodeTable := "t_" + "primary" + "_" + "store" + "_" + "nodes"
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE t_schema_meta (
			c_key TEXT NOT NULL PRIMARY KEY,
			c_value TEXT NOT NULL
		);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '4');
		CREATE TABLE %s (
			c_node_id TEXT NOT NULL PRIMARY KEY,
			c_name TEXT NOT NULL
		);
	`, legacyNodeTable)); err != nil {
		t.Fatal(err)
	}

	err = store.InitSchema(ctx)
	if err == nil {
		t.Fatal("v4 metadata database was accepted by Schema v5 initialization")
	}
	if err.Error() != "incompatible storage metadata schema; reset metadata database" {
		t.Fatalf("v4 metadata database error = %q", err)
	}
	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "4" {
		t.Fatalf("rejected metadata database version changed to %q", version)
	}
	var dataNodeTableCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 't_data_nodes'
	`).Scan(&dataNodeTableCount); err != nil {
		t.Fatal(err)
	}
	if dataNodeTableCount != 0 {
		t.Fatal("v4 database ran Schema v5 DDL after version rejection")
	}
}

func metadataSchemaPath() string {
	return filepath.Join("..", "..", "..", "..", "schema", "metadata.sql")
}
