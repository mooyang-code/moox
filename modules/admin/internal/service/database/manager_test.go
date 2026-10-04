package database

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestMinInt(t *testing.T) {
	assert.Equal(t, 1, minInt(1, 2))
	assert.Equal(t, 1, minInt(2, 1))
	assert.Equal(t, -3, minInt(-3, 0))
	assert.Equal(t, 5, minInt(5, 5))
}

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

func TestInitializeUsesBoundedSQLitePool(t *testing.T) {
	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "admin.db")}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	sqlDB, err := mgr.GetDB().DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	defer sqlDB.Close()
	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 8 {
		t.Fatalf("MaxOpenConnections = %d, want 8", stats.MaxOpenConnections)
	}
}
