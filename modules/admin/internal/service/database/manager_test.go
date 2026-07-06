package database

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{Path: dbPath}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	sqlDB, err := mgr.GetDB().DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	defer sqlDB.Close()

	var count int64
	if err := mgr.GetDB().Raw(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'table'
  AND name IN ('t_spaces', 't_users', 't_ssh_host', 't_secrets')
`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Initialize() created %d admin tables, want 0", count)
	}
}
