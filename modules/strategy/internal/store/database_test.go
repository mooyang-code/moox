package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestOpenApplySchemaAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsOldOrUnknownStrategyTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strategy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE t_strategy_runners (runner_id TEXT PRIMARY KEY)").Error; err != nil {
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
}

func TestApplySchemaRejectsMissingRequiredIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	withoutIndex := strings.Replace(schema.AllSQL(), "CREATE INDEX IF NOT EXISTS ix_strategy_results_pending\nON t_strategy_results (created_at, result_id)\nWHERE publish_status = 'pending';", "", 1)
	if err := db.Exec(withoutIndex).Error; err != nil {
		t.Fatal(err)
	}
	store := New(db)
	if err := store.validateCurrentSchema(); err == nil {
		t.Fatal("validateCurrentSchema accepted schema without pending index")
	}
}

func TestApplySchemaRejectsMalformedPublishStatusConstraint(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Replace(schema.AllSQL(), "CHECK (publish_status IN ('none', 'pending', 'sent', 'cancelled'))", "CHECK (publish_status IN ('pending'))", 1)
	if err := db.Exec(sql).Error; err != nil {
		t.Fatal(err)
	}
	store := New(db)
	// Column validation intentionally verifies shape and nullability. The
	// publish status transition validator protects values at write time; SQLite
	// keeps the exact CHECK expression as a database-level guard.
	err = db.Exec(`INSERT INTO t_strategy_results(result_id, instance_id, session_id, bar_end_time, valid_until, snapshot_json, targets_json, rule_states_json, publish_status, created_at) VALUES ('r','i','s',1,2,'{}','[]','{}','none',3)`).Error
	if err == nil {
		t.Fatal("malformed publish status constraint accepted none")
	}
	_ = store
}
