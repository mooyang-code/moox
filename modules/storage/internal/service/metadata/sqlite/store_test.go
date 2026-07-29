package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetadataSchemaVersionIsExact(t *testing.T) {
	for _, version := range []string{"", "1", "2", "3", "4", "5", "7"} {
		if version == metadataSchemaVersion {
			t.Fatalf("test case %q unexpectedly equals current schema version", version)
		}
	}
	if metadataSchemaVersion != "6" {
		t.Fatalf("metadata schema version = %q, want 6", metadataSchemaVersion)
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
	if version != "6" {
		t.Fatalf("fresh database schema version = %q, want 6", version)
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

func metadataSchemaPath() string {
	return filepath.Join("..", "..", "..", "..", "schema", "metadata.sql")
}
