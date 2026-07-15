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
	assertTableExists(t, dbPath, "t_gateway_nodes")
	assertTableExists(t, dbPath, "t_service_deployments")
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestRunInitCommandCreatesNodeScopedDeploymentConstraints(t *testing.T) {
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

	insertNode := `INSERT INTO t_gateway_nodes(c_node_id, c_name, c_public_address) VALUES (?, ?, ?)`
	if err := db.Exec(insertNode, "node-a", "Node A", "https://node-a.example").Error; err != nil {
		t.Fatalf("insert node with nullable host: %v", err)
	}
	if err := db.Exec(insertNode, "node-b", "Node B", "https://node-b.example").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO t_gateway_nodes(c_node_id, c_host_id, c_name, c_public_address) VALUES ('bad-node', 999, 'Bad', 'https://bad.example')`).Error; err == nil {
		t.Fatal("gateway node with unknown host must violate its foreign key")
	}

	insertDeployment := `INSERT INTO t_service_deployments(c_node_id, c_service_name, c_gateway_service_id, c_gateway_enabled) VALUES (?, ?, ?, ?)`
	if err := db.Exec(insertDeployment, "node-a", "eventbus", "gateway-eventbus", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(insertDeployment, "node-b", "eventbus", "gateway-eventbus", 1).Error; err != nil {
		t.Fatalf("same service name and gateway ID on another node must be accepted: %v", err)
	}
	if err := db.Exec(insertDeployment, "node-a", "eventbus", "other-id", 1).Error; err == nil {
		t.Fatal("duplicate service name on one node must be rejected")
	}
	if err := db.Exec(insertDeployment, "node-a", "archive", "gateway-eventbus", 1).Error; err == nil {
		t.Fatal("duplicate enabled gateway service ID on one node must be rejected")
	}
	if err := db.Exec(insertDeployment, "missing-node", "archive", "gateway-archive", 1).Error; err == nil {
		t.Fatal("deployment with unknown node must violate its foreign key")
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
