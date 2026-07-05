package storage

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"gorm.io/gorm"
)

func TestInitializeMigratesExistingJobItemProjectionColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := db.Exec(`
CREATE TABLE t_cloud_job_items (
  c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_job_item_id TEXT NOT NULL,
  c_status TEXT NOT NULL DEFAULT 'pending',
  c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
`).Error; err != nil {
		t.Fatalf("seed old table: %v", err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{Path: dbPath}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	defer mgr.Close()
	for _, column := range []string{"c_queue_subject", "c_enqueue_status", "c_cancel_reason"} {
		if !mgr.DB().Migrator().HasColumn("t_cloud_job_items", column) {
			t.Fatalf("column %s was not migrated", column)
		}
	}
}

func TestInitializeCapsSQLitePoolToSingleConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr := NewManager()
	if err := mgr.Initialize(&config.DatabaseConfig{
		Path:         dbPath,
		MaxOpenConns: 50,
		MaxIdleConns: 10,
	}); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	defer mgr.Close()

	sqlDB, err := mgr.DB().DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}
