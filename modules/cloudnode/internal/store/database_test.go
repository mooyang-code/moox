package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

func TestInitializeDoesNotMutateExistingSchema(t *testing.T) {
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

	mgr, err := Open(&config.DatabaseConfig{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer mgr.Close()
	for _, column := range []string{"c_queue_subject", "c_enqueue_status"} {
		if mgr.db.Migrator().HasColumn("t_cloud_job_items", column) {
			t.Fatalf("Initialize() added column %s, want schema unchanged", column)
		}
	}
}

func TestMigrateLegacySchemaAddsNodeLifecycleColumnsWithoutChangingRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy-cloudnode.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	require.NoError(t, db.Exec(`CREATE TABLE t_cloud_nodes (
		c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		c_space_id TEXT NOT NULL DEFAULT '',
		c_node_id TEXT NOT NULL,
		c_metadata TEXT NOT NULL DEFAULT '{}',
		c_is_deleted INTEGER NOT NULL DEFAULT 0
		); INSERT INTO t_cloud_nodes (c_space_id, c_node_id, c_metadata) VALUES ('crypto', 'legacy', '{}');`).Error)

	mgr := &Store{db: db}
	require.NoError(t, mgr.MigrateLegacySchema())
	for _, column := range []string{"c_lifecycle_id", "c_lifecycle_operation_id"} {
		require.True(t, db.Migrator().HasColumn("t_cloud_nodes", column), "migration must add %s", column)
	}
	var count int64
	require.NoError(t, db.Model(&CloudNode{}).Where("c_node_id = ?", "legacy").Count(&count).Error)
	require.EqualValues(t, 1, count, "adding lifecycle columns must preserve legacy rows")
}

func TestMigrateLegacySchemaAddsPublishLeaseRecoveryOwnership(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy-publish-lease.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_cloud_node_batch_items (
		c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		c_space_id TEXT NOT NULL,
		c_job_id TEXT NOT NULL,
		c_item_id TEXT NOT NULL,
		c_request_json TEXT NOT NULL
	)`).Error)

	store := &Store{db: db}
	require.NoError(t, store.MigrateLegacySchema())
	require.True(t, db.Migrator().HasColumn("t_cloud_node_batch_items", "c_publish_lease_recovery_owned"))
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
