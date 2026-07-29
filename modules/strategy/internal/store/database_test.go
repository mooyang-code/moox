package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestOpenApplySchemaAndClose(t *testing.T) {
	mgr, err := Open(filepath.Join(t.TempDir(), "strategy.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	if mgr.db == nil {
		t.Fatal("DB() returned nil")
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenRejectsObsoleteStrategySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE t_strategy_bindings (c_binding_id TEXT PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open() accepted an obsolete Strategy schema")
	}
	for _, want := range []string{"t_strategy_bindings", "删除旧数据库后重建"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Open() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestOpenAcceptsCurrentSchemaOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopened Open() error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("reopened Close() error = %v", err)
	}
}
