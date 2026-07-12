package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func TestIsInitCommand(t *testing.T) {
	assert.False(t, isInitCommand(nil))
	assert.False(t, isInitCommand([]string{"moox-collector"}))
	assert.True(t, isInitCommand([]string{"moox-collector", "init"}))
}

func TestPrintInitError(t *testing.T) {
	var buf bytes.Buffer
	printInitError(&buf, errors.New("schema failed"))
	assert.Contains(t, buf.String(), "init_failed")
	assert.Contains(t, buf.String(), "schema failed")
	printInitError(nil, errors.New("ignored"))
}

func TestRunInitCommandAppliesCollectorSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	assertTableExists(t, dbPath, "t_collector_task_instances")
	assertTableNotExists(t, dbPath, "t_collector_execution_logs")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestRunInitCommandDropsLegacyExecutionLogTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec("CREATE TABLE t_collector_execution_logs (c_id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	assertTableNotExists(t, dbPath, "t_collector_execution_logs")
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

func assertTableNotExists(t *testing.T, dbPath string, tableName string) {
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
