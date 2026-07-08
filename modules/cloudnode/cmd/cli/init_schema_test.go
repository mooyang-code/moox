package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRunInitCommandAppliesCloudNodeSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	seedLegacyJobItemTables(t, dbPath)

	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	assertTableMissing(t, dbPath, "t_cloud_job_items")
	assertTableMissing(t, dbPath, "t_cloud_job_item_attempts")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func seedLegacyJobItemTables(t *testing.T, dbPath string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, stmt := range []string{
		"CREATE TABLE t_cloud_job_items (c_id INTEGER PRIMARY KEY, c_job_item_id TEXT)",
		"CREATE TABLE t_cloud_job_item_attempts (c_id INTEGER PRIMARY KEY, c_job_item_id TEXT)",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("seed legacy table: %v", err)
		}
	}
}

func assertTableMissing(t *testing.T, dbPath string, tableName string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	var count int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tableName).Scan(&count).Error; err != nil {
		t.Fatalf("query table %s: %v", tableName, err)
	}
	if count != 0 {
		t.Fatalf("table %s exists = %d, want 0", tableName, count)
	}
}
