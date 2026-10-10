package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
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
	assertTableExists(t, dbPath, "t_hosts")
	assertTableExists(t, dbPath, "t_placements")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestRunInitCommandIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	for attempt := 1; attempt <= 2; attempt++ {
		if err := runInitCommand([]string{"init", "--db-path", dbPath}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("runInitCommand() attempt %d error = %v", attempt, err)
		}
	}

	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range []struct {
		typ  string
		name string
	}{
		{typ: "table", name: "t_hosts"},
		{typ: "table", name: "t_placements"},
		{typ: "table", name: "t_host_gateway_status"},
		{typ: "index", name: "idx_t_placements_component"},
	} {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = ? AND name = ?", object.typ, object.name).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s %s count = %d, want 1", object.typ, object.name, count)
		}
	}
	var foreignKeyErrors int64
	if err := db.Raw("SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&foreignKeyErrors).Error; err != nil {
		t.Fatal(err)
	}
	if foreignKeyErrors != 0 {
		t.Fatalf("foreign_key_check returned %d errors", foreignKeyErrors)
	}
	var integrity string
	if err := db.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q, want ok", integrity)
	}
}

// TestRunInitCommandCreatesPlacementConstraints 校验部署表的约束：部署必须属于已登记的主机，同一主机上一个组件只登记一次，
// 主机地址唯一，状态只能是 enabled 或 disabled。
func TestRunInitCommandCreatesPlacementConstraints(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInitCommand() error = %v", err)
	}
	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", foreignKeys, err)
	}

	insertHost := `INSERT INTO t_hosts(c_host_id, c_address) VALUES (?, ?)`
	if err := db.Exec(insertHost, "control", "106.53.107.122").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(insertHost, "storage", "106.53.107.122").Error; err == nil {
		t.Fatal("two hosts with the same address must be rejected")
	}
	if err := db.Exec(`INSERT INTO t_hosts(c_host_id, c_address, c_status) VALUES ('bad', '192.0.2.1', 'paused')`).Error; err == nil {
		t.Fatal("unknown host status must be rejected")
	}

	insertPlacement := `INSERT INTO t_placements(c_host_id, c_component_id) VALUES (?, ?)`
	if err := db.Exec(insertPlacement, "control", "admin").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(insertPlacement, "control", "admin").Error; err == nil {
		t.Fatal("duplicate placement on one host must be rejected")
	}
	if err := db.Exec(insertPlacement, "missing", "admin").Error; err == nil {
		t.Fatal("placement on an unknown host must violate its foreign key")
	}
	if err := db.Exec(`UPDATE t_placements SET c_status = 'paused' WHERE c_host_id = 'control'`).Error; err == nil {
		t.Fatal("unknown placement status must be rejected")
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
