package sqlite

import (
	"context"
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

func TestMetadataSchemaV4DatabaseIsRejected(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.db.ExecContext(ctx, `
		CREATE TABLE t_schema_meta (
			c_key TEXT NOT NULL PRIMARY KEY,
			c_value TEXT NOT NULL
		);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '4');
	`); err != nil {
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
}
