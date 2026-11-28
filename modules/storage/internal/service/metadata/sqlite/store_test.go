package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/sqlitedb"
)

func TestInitSchemaAcceptsFreshDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, Options{
		Path: filepath.Join(dir, "metadata.db"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("fresh database InitSchema: %v", err)
	}
	if err := store.ValidateSchema(ctx); err != nil {
		t.Fatalf("fresh database ValidateSchema: %v", err)
	}
	if err := RecordPrimaryFormat(dir, false); err != nil {
		t.Fatalf("RecordPrimaryFormat: %v", err)
	}
	rec, err := dataformat.ReadFile(filepath.Join(dir, dataformat.FileName))
	if err != nil {
		t.Fatalf("ReadFile data-format: %v", err)
	}
	if rec.Component != "storage-primary" {
		t.Fatalf("component = %q, want storage-primary", rec.Component)
	}
	for _, key := range []string{dataformat.PartSQLiteSchema, dataformat.PartStoragePebbleLayout, dataformat.PartEvents} {
		if rec.Parts[key] == "" {
			t.Fatalf("missing part %q in storage-primary data-format", key)
		}
	}

	// 已有数据且格式文件事件摘要不同时，RecordPrimaryFormat 必须拒绝并提示 setup install --wipe。
	stale, err := dataformat.Build("storage-primary", map[string]string{
		dataformat.PartSQLiteSchema:        rec.Parts[dataformat.PartSQLiteSchema],
		dataformat.PartStoragePebbleLayout: rec.Parts[dataformat.PartStoragePebbleLayout],
		dataformat.PartEvents:              "sha256:stale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dataformat.WriteDir(dir, stale); err != nil {
		t.Fatal(err)
	}
	if err := RecordPrimaryFormat(dir, true); !errors.Is(err, dataformat.ErrIncompatibleFormat) || !strings.Contains(err.Error(), "setup install --wipe") {
		t.Fatalf("RecordPrimaryFormat with stale events digest = %v, want ErrIncompatibleFormat", err)
	}
}

func TestInitSchemaRejectsObsoleteSchemaWithRebuildInstructions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "metadata.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE t_schema_meta (c_key TEXT NOT NULL PRIMARY KEY, c_value TEXT NOT NULL);
		INSERT INTO t_schema_meta (c_key, c_value) VALUES ('schema_version', '13');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	err = store.InitSchema(ctx)
	if !errors.Is(err, sqlitedb.ErrIncompatibleSchema) {
		t.Fatalf("InitSchema error = %v, want ErrIncompatibleSchema", err)
	}
	if !strings.Contains(err.Error(), "全新部署") {
		t.Fatalf("InitSchema error %q missing 全新部署 hint", err)
	}
	err = store.ValidateSchema(ctx)
	if !errors.Is(err, sqlitedb.ErrIncompatibleSchema) {
		t.Fatalf("ValidateSchema error = %v, want ErrIncompatibleSchema", err)
	}
}
