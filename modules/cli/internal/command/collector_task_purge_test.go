package command

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCollectorTaskPurgeDefaultsToReadOnlyInventory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_task_instances (c_id INTEGER PRIMARY KEY, c_space_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_fetch_batches (c_id INTEGER PRIMARY KEY, c_space_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_fetch_retry_items (c_id INTEGER PRIMARY KEY, c_space_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_tasks VALUES (1, 'crypto', 'task-1', 'BTC', 'view-1', 'dataset-1')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_task_instances VALUES (1, 'crypto')`).Error)

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{DBPath: dbPath, SpaceID: "crypto"})
	require.NoError(t, err)
	require.True(t, summary.DryRun)
	require.Equal(t, "dry_run", summary.Status)
	require.EqualValues(t, 1, summary.TaskCount)
	require.EqualValues(t, 1, summary.TaskInstanceCount)
	require.Len(t, summary.Results, 1)
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM t_collector_tasks`).Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestCollectorTaskPurgeApplyRequiresConfirmation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)

	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{DBPath: dbPath, Apply: true})
	require.Error(t, err)
	require.ErrorContains(t, err, "--apply and --confirm")
	require.Equal(t, "dry_run", summary.Status)
	require.FileExists(t, dbPath)
}

func TestCollectorTaskPurgeApplyBacksUpBeforeInitializing(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_space_id TEXT, c_task_id TEXT, c_task_name TEXT, c_result_view_id TEXT, c_result_dataset_id TEXT)`).Error)

	initScript := filepath.Join(t.TempDir(), "collector-init.sh")
	require.NoError(t, os.WriteFile(initScript, []byte("#!/bin/sh\n[ \"$1\" = init ] && [ \"$2\" = --db-path ] && [ -n \"$3\" ] || exit 2\nexit 0\n"), 0o700))
	summary, err := runCollectorTaskPurge(context.Background(), collectorTaskPurgeFlags{
		DBPath:           dbPath,
		Apply:            true,
		Confirm:          true,
		StopCommand:      "true",
		CollectorInitBin: initScript,
	})
	require.NoError(t, err)
	require.Equal(t, "applied", summary.Status)
	require.False(t, summary.DryRun)
	require.NoFileExists(t, dbPath)
	require.DirExists(t, filepath.Dir(summary.BackupPath))
	require.FileExists(t, summary.BackupPath)
	require.Equal(t, []string{"collector_writes_stopped", "collector_database_backed_up", "collector_schema_initialized"}, summary.CompletedStages)
}
