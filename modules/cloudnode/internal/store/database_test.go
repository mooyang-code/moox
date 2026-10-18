package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"gorm.io/gorm/logger"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr, err := Open(&config.DatabaseConfig{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer mgr.Close()

	var count int64
	if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 't_cloud_%'").Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Initialize() created %d cloudnode tables, want 0", count)
	}
}

func TestInitializeCapsSQLitePoolToSingleConnection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr, err := Open(&config.DatabaseConfig{
		Path:         dbPath,
		MaxOpenConns: 50,
		MaxIdleConns: 10,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer mgr.Close()

	sqlDB, err := mgr.db.DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
}

func TestOpenDisablesGORMQueryLogging(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr, err := Open(&config.DatabaseConfig{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer mgr.Close()

	if mgr.db.Config.Logger != logger.Discard {
		t.Fatal("GORM logger must discard SQL because batch request_json contains credentials")
	}
}

func TestOpenRestrictsDatabaseFileToOwner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr, err := Open(&config.DatabaseConfig{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer mgr.Close()

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database permissions = %04o, want 0600", got)
	}
}
