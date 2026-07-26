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

func metadataSchemaPath() string {
	return filepath.Join("..", "..", "..", "..", "schema", "metadata.sql")
}
