package store

import (
	"path/filepath"
	"testing"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr := NewManager()
	if err := mgr.Initialize(&Options{Path: dbPath}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.DB().DB(); err != nil {
		t.Fatalf("DB() error = %v", err)
	}

	var count int64
	if err := mgr.DB().Raw(`
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
