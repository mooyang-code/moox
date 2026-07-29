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

func TestOpenRejectsMalformedCurrentStrategySchema(t *testing.T) {
	tests := map[string]func(string) string{
		"missing primary key": func(sql string) string {
			return strings.Replace(sql, "strategy_id TEXT PRIMARY KEY", "strategy_id TEXT NOT NULL", 1)
		},
		"missing not null": func(sql string) string {
			return strings.Replace(sql, "source_hash TEXT NOT NULL", "source_hash TEXT", 1)
		},
		"missing result logical unique": func(sql string) string {
			return strings.Replace(sql, ",\n    UNIQUE (runner_id, strategy_id, namespace, trigger_bar_time)", "", 1)
		},
		"wrong runner partial unique predicate": func(sql string) string {
			return strings.Replace(sql, "status = 'ENABLED'", "status = 'DISABLED'", 1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "strategy.db")
			db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(mutate(schema.AllSQL())).Error; err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "删除旧数据库后重建") {
				t.Fatalf("Open() error = %v", err)
			}
		})
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
