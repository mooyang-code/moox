package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIsInitCommand_ShouldDetectInitSubcommand(t *testing.T) {
	assert.True(t, isInitCommand([]string{"moox-cloudnode", "init"}))
	assert.False(t, isInitCommand([]string{"moox-cloudnode", "serve"}))
}

func TestPrintInitError_ShouldWriteJSON(t *testing.T) {
	var stderr bytes.Buffer
	printInitError(&stderr, assert.AnError)
	assert.Contains(t, stderr.String(), "init_failed")
}

func TestRunInitCommandAppliesCloudNodeSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runInitCommand() error = %v, stderr = %s", err, stderr.String())
	}
	if stdout.String() == "" {
		t.Fatalf("runInitCommand() wrote empty stdout")
	}
}

func TestRunInitCommandRemovesDeprecatedNodeFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
CREATE TABLE t_cloud_nodes (
  c_id INTEGER NOT NULL PRIMARY KEY,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_node_id TEXT NOT NULL DEFAULT '',
  c_provider TEXT NOT NULL DEFAULT 'tencent-scf',
  c_cloud_account_id TEXT NOT NULL DEFAULT '',
  c_package_id TEXT NOT NULL DEFAULT '',
  c_package_version TEXT NOT NULL DEFAULT '',
  c_deployment_id TEXT NOT NULL DEFAULT '',
  c_node_type TEXT NOT NULL DEFAULT 'scf-event',
  c_region TEXT NOT NULL DEFAULT '',
  c_namespace TEXT NOT NULL DEFAULT '',
  c_function_name TEXT NOT NULL DEFAULT '',
  c_metadata TEXT NOT NULL DEFAULT '{}',
  c_status TEXT NOT NULL DEFAULT 'unknown',
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO t_cloud_nodes (c_id, c_metadata) VALUES (1, '{"cls_topic_id":"topic-1","cls_logset_id":"logset-1","tag":"海外"}');
`).Error)

	var stdout bytes.Buffer
	require.NoError(t, runInitCommand([]string{"init", "--db-path", dbPath}, &stdout, nil))
	assert.False(t, db.Migrator().HasColumn("t_cloud_nodes", "c_status"))

	var metadata string
	require.NoError(t, db.Raw("SELECT c_metadata FROM t_cloud_nodes WHERE c_id = 1").Scan(&metadata).Error)
	assert.JSONEq(t, `{"tag":"海外"}`, metadata)
}
