package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestIsInitCommand_ShouldDetectInitSubcommand(t *testing.T) {
	assert.True(t, isInitCommand([]string{"moox-admin", "init"}))
	assert.False(t, isInitCommand([]string{"moox-admin", "serve"}))
}

func TestPrintInitError_ShouldWriteJSON(t *testing.T) {
	var stderr bytes.Buffer
	printInitError(&stderr, assert.AnError)
	assert.Contains(t, stderr.String(), "init_failed")
}

func TestRunInitCommandAppliesAdminSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	assertTableExists(t, dbPath, "t_users")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestApplySchemaMigratesLegacyServiceDeployments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	legacySQL := `
CREATE TABLE t_service_deployments (
  c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  c_service_name TEXT NOT NULL,
  c_service_kind TEXT NOT NULL DEFAULT '',
  c_protocol TEXT NOT NULL DEFAULT 'http',
  c_host TEXT NOT NULL DEFAULT '',
  c_port INTEGER NOT NULL DEFAULT 0,
  c_base_url TEXT NOT NULL DEFAULT '',
  c_rpc_address TEXT NOT NULL DEFAULT '',
  c_gateway_path TEXT NOT NULL DEFAULT '',
  c_scope TEXT NOT NULL DEFAULT 'public',
  c_status TEXT NOT NULL DEFAULT 'active',
  c_description TEXT NOT NULL DEFAULT '',
  c_extra_config TEXT NOT NULL DEFAULT '{}',
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_service_deployments_name_deleted
ON t_service_deployments(c_service_name, c_is_deleted);
INSERT INTO t_service_deployments(c_service_name, c_host, c_port, c_is_deleted)
VALUES ('service-a', '127.0.0.1', 10001, 0), ('service-a', '127.0.0.9', 10009, 1);`
	if err := db.Exec(legacySQL).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	if err := applySchema(dbPath, adminschema.AdminSQL()); err != nil {
		t.Fatalf("applySchema() migration error = %v", err)
	}
	migrated, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ServiceName string `gorm:"column:c_service_name"`
		Host        string `gorm:"column:c_host"`
	}
	if err := migrated.Table("t_service_deployments").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ServiceName != "service-a" || rows[0].Host != "127.0.0.1" {
		t.Fatalf("migrated rows = %+v", rows)
	}
	for _, removed := range []string{"c_is_deleted", "c_base_url", "c_rpc_address"} {
		var count int64
		if err := migrated.Raw("SELECT COUNT(*) FROM pragma_table_info('t_service_deployments') WHERE name = ?", removed).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("legacy column %s remains", removed)
		}
	}
}

func assertTableExists(t *testing.T, dbPath string, tableName string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tableName).Scan(&count).Error; err != nil {
		t.Fatalf("query table %s: %v", tableName, err)
	}
	if count != 1 {
		t.Fatalf("table %s exists = %d, want 1", tableName, count)
	}
}
