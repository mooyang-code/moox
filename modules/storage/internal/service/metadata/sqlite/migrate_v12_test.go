package sqlite

import (
	"context"
	"testing"
)

func TestMigrateV11ToV12CreatesTagTablesBeforeVersionValidation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, ctx)

	for _, statement := range []string{
		`DROP TABLE t_subject_tags`,
		`DROP TABLE t_tags`,
		`UPDATE t_schema_meta SET c_value = '11' WHERE c_key = 'schema_version'`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := store.migrateV11ToV12(ctx); err != nil {
		t.Fatal(err)
	}

	var version string
	if err := store.db.QueryRowContext(ctx, `SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "12" {
		t.Fatalf("schema version = %q, want 12", version)
	}
	for _, table := range []string{"t_tags", "t_subject_tags"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %s was not created by migration", table)
		}
	}
}
