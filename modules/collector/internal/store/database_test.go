package store

import (
	"path/filepath"
	"testing"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.db.DB(); err != nil {
		t.Fatalf("DB() error = %v", err)
	}

	var count int64
	if err := mgr.db.Raw(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'table'
  AND name LIKE 't_collector_%'
`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Initialize() created %d collector tables, want 0", count)
	}
}
