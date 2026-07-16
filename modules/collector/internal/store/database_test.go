package store

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.db.DB(); err != nil {
		t.Fatalf("DB() error = %v", err)
	}

	var count int64
	if err := mgr.db.Raw(`
SELECT count(*)
FROM sqlite_master
WHERE type = 'table'
  AND name LIKE 't_collector_%'
`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Initialize() created %d collector tables, want 0", count)
	}
}

func TestApplySchemaDropsPlannedExecNodeWithoutLosingTasks(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.db.Exec(`
CREATE TABLE t_collector_task_instances (
  c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  c_space_id TEXT NOT NULL DEFAULT '',
  c_task_id TEXT NOT NULL,
  c_cloud_job_item_id TEXT NOT NULL DEFAULT '',
  c_rule_id TEXT NOT NULL,
  c_exchange TEXT NOT NULL DEFAULT '',
  c_market TEXT NOT NULL DEFAULT '',
  c_data_type TEXT NOT NULL DEFAULT '',
  c_dataset_id TEXT NOT NULL DEFAULT '',
  c_subject_id TEXT NOT NULL DEFAULT '',
  c_symbol TEXT NOT NULL DEFAULT '',
  c_interval TEXT NOT NULL DEFAULT 'default',
  c_planned_exec_node TEXT NOT NULL DEFAULT '',
  c_last_exec_node TEXT NOT NULL DEFAULT '',
  c_last_exec_status INTEGER NOT NULL DEFAULT 1,
  c_task_params TEXT NOT NULL DEFAULT '{}',
  c_last_exec_time DATETIME,
  c_result TEXT NOT NULL DEFAULT '{}',
  c_is_deleted INTEGER NOT NULL DEFAULT 0,
  c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
  c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_collector_instances_exec
  ON t_collector_task_instances(c_planned_exec_node, c_last_exec_status);
INSERT INTO t_collector_task_instances(c_task_id, c_rule_id, c_planned_exec_node, c_last_exec_status)
VALUES ('task-1', 'rule-1', '', 3);
`).Error)

	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))

	var columns []struct{ Name string }
	require.NoError(t, mgr.db.Raw("PRAGMA table_info(t_collector_task_instances)").Scan(&columns).Error)
	for _, column := range columns {
		assert.NotEqual(t, "c_planned_exec_node", column.Name)
	}

	var status int
	require.NoError(t, mgr.db.Raw(`
SELECT c_last_exec_status
FROM t_collector_task_instances
WHERE c_task_id = 'task-1'
`).Scan(&status).Error)
	assert.Equal(t, 3, status)

	var indexColumns []struct{ Name string }
	require.NoError(t, mgr.db.Raw("PRAGMA index_info(idx_collector_instances_exec)").Scan(&indexColumns).Error)
	require.Len(t, indexColumns, 1)
	assert.Equal(t, "c_last_exec_status", indexColumns[0].Name)
}
