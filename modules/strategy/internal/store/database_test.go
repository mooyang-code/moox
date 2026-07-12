package store

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/schema"
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
