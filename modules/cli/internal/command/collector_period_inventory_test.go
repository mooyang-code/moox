package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCollectorPeriodInventoryIsReadOnlyAndRedactsSeriesMembers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`PRAGMA journal_mode=WAL`).Error)
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	schemaSQL, err := os.ReadFile(filepath.Join(repoRoot, "modules/collector/schema/collector.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(schemaSQL)).Error)
	seedCollectorPeriodInventoryFixture(t, db)

	before := snapshotInventoryFixtureFiles(t, filepath.Dir(dbPath))
	require.Contains(t, before, filepath.Base(dbPath)+"-wal")
	require.Contains(t, before, filepath.Base(dbPath)+"-shm")
	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	require.True(t, report.ReadOnly)
	require.True(t, report.SnapshotConsistent)
	require.True(t, report.Complete)
	require.Equal(t, "crypto", report.SpaceID)
	require.EqualValues(t, 1, report.TableCounts["t_collector_tasks"])
	require.EqualValues(t, 1, report.StateCounts["tasks.enabled"]["enabled"])
	require.Len(t, report.ActiveFetchBatches, 3)
	hasDispatchedRealtime := false
	for _, batch := range report.ActiveFetchBatches {
		if batch.Kind == "realtime" && batch.Status == "dispatched" {
			hasDispatchedRealtime = true
		}
	}
	require.True(t, hasDispatchedRealtime)
	require.Len(t, report.PendingRetries, 1)
	require.Equal(t, []string{"bars-1m"}, report.PendingRetries[0].DatasetIDs)
	require.Equal(t, "permanent_failed", report.PendingRetries[0].Status)
	require.Equal(t, "pending", report.PendingRetries[0].FailureReportState)
	require.NotEqual(t, "retry-a", report.PendingRetries[0].RetryRef)
	require.Len(t, report.PeriodKeys, 1)
	period := report.PeriodKeys[0]
	require.Equal(t, "bars-1m", period.DatasetID)
	require.Equal(t, "1m", period.Frequency)
	require.Equal(t, "2026-10-01T00:00:00Z", period.PeriodTime)
	require.EqualValues(t, 2, period.SnapshotSeriesRows)
	require.EqualValues(t, 2, period.SnapshotExpectedCountMin)
	require.EqualValues(t, 2, period.SnapshotExpectedCountMax)
	require.True(t, period.SnapshotValid)
	require.True(t, period.SnapshotRequired)
	require.True(t, period.TimerManifestValid)
	require.Equal(t, "waiting", period.StorageStatus)
	require.Equal(t, "waiting", period.ReadinessStatus)
	require.Equal(t, "pending", period.ReadinessReportState)
	require.EqualValues(t, 2, period.TimerBatchCount)
	require.EqualValues(t, 1, period.TimerClaimedBatchCount)

	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-subject-marker")
	require.NotContains(t, string(encoded), "private-symbol-marker")
	require.NotContains(t, string(encoded), "batch-a")
	require.NotContains(t, string(encoded), "retry-a")
	after := snapshotInventoryFixtureFiles(t, filepath.Dir(dbPath))
	require.Equal(t, before, after, "read-only inventory must not modify the database or WAL; SQLite may update SHM read marks")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

func TestCollectorPeriodInventoryRejectsIncompleteSchemaAndReportsMissingTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE t_collector_tasks (c_space_id TEXT)`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotEmpty(t, report.Schema.MissingTables)
	require.NotEmpty(t, report.Schema.MissingColumns)
	require.ErrorContains(t, err, "incomplete")
}

func TestCollectorPeriodInventoryRequiresExplicitScopeAndAbsoluteDatabasePath(t *testing.T) {
	_, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  "relative.db",
		SpaceID: " ",
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "space id")
}

func TestCollectorPeriodInventoryFailsClosedForUnknownSpaceAndKeepsEmptyArrays(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypt0",
	})
	require.Error(t, err)
	require.False(t, report.Complete)

	db, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DELETE FROM t_period_readiness_items WHERE c_readiness_id IN
		(SELECT c_id FROM t_period_readiness WHERE c_space_id = 'crypto')`).Error)
	for _, table := range []string{"t_collector_task_period_series", "t_period_readiness", "t_collector_period_storage_states", "t_collector_timer_period_batches"} {
		require.NoError(t, db.Exec("DELETE FROM "+table+" WHERE c_space_id = 'crypto'").Error)
	}
	sqlDB, err = db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err = runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	encoded := collectorPeriodInventoryJSON(t, report)
	require.Contains(t, encoded, `"period_keys":[]`)
}

func TestCollectorPeriodInventoryDoesNotRequireCollectorSnapshotForResampleReadiness(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	for index, period := range []struct{ datasetID, frequency, at string }{
		{datasetID: "derived-1h", frequency: "1H", at: "2026-10-01 01:00:00"},
		{datasetID: "derived-4h", frequency: "4H", at: "2026-10-01 02:00:00"},
		{datasetID: "derived-7m", frequency: "7m", at: "2026-10-01 03:00:00"},
	} {
		require.NoError(t, db.Exec(`INSERT INTO t_period_readiness
			(c_space_id,c_dataset_id,c_frequency,c_work_type,c_period_time,c_deadline_at,c_status,c_report_state)
			VALUES ('crypto',?,?, 'resample', ?, '2026-10-01 04:02:00','complete','reported')`, period.datasetID, period.frequency, period.at).Error)
		require.NoError(t, db.Exec(`INSERT INTO t_period_readiness_items
			(c_readiness_id,c_instance_id,c_write_target_id,c_subject_id,c_function_name,c_write_source,c_state,c_updated_at)
			VALUES ((SELECT c_id FROM t_period_readiness WHERE c_space_id = 'crypto' AND c_dataset_id = ?),
			?,?,'private-subject-marker','collector_local_resample','collector:kline_resample','success','2026-10-01 04:01:00')`,
			period.datasetID, fmt.Sprintf("resample-instance-%d", index), fmt.Sprintf("resample-target-%d", index)).Error)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	seen := make(map[string]bool)
	for i := range report.PeriodKeys {
		period := report.PeriodKeys[i]
		if strings.HasPrefix(period.DatasetID, "derived-") {
			require.Equal(t, "resample", period.WorkType)
			require.False(t, period.SnapshotRequired)
			seen[period.DatasetID] = true
		}
	}
	require.Equal(t, map[string]bool{"derived-1h": true, "derived-4h": true, "derived-7m": true}, seen)
	require.True(t, report.Complete)
}

func TestCollectorPeriodInventoryRetainsSnapshotSubjectsOnlyForTimerPeriods(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	var rawPeriodTime string
	require.NoError(t, tx.Raw(`SELECT c_period_time FROM t_collector_task_period_series WHERE c_space_id = 'crypto' LIMIT 1`).Scan(&rawPeriodTime).Error)
	period := collectorPeriodInventoryPeriod{
		DatasetID: "bars-1m", Frequency: "1m", RawPeriodTime: rawPeriodTime, SnapshotSeriesRows: 2,
	}
	subjects, err := validateCollectorPeriodInventorySnapshotContents(context.Background(), tx, "crypto", []collectorPeriodInventoryPeriod{period})
	require.NoError(t, err)
	require.Empty(t, subjects)

	period.TimerBatchCount = 1
	subjects, err = validateCollectorPeriodInventorySnapshotContents(context.Background(), tx, "crypto", []collectorPeriodInventoryPeriod{period})
	require.NoError(t, err)
	require.Len(t, subjects[inventoryPeriodKey(period.DatasetID, period.Frequency, period.RawPeriodTime)], 2)
	require.NoError(t, tx.Rollback().Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

func TestCollectorPeriodInventoryRejectsInvalidSnapshotIdentity(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "missing provider symbol", query: `UPDATE t_collector_task_period_series SET c_provider_symbol = '' WHERE c_space_id = 'crypto'`},
		{name: "unsupported frequency", query: `UPDATE t_collector_task_period_series SET c_frequency = 'fortnight' WHERE c_space_id = 'crypto'`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			require.NoError(t, db.Exec(test.query).Error)
			if test.name == "unsupported frequency" {
				require.NoError(t, db.Exec(`DELETE FROM t_period_readiness WHERE c_space_id = 'crypto' AND c_dataset_id = 'bars-1m'`).Error)
				require.NoError(t, db.Exec(`DELETE FROM t_collector_period_storage_states WHERE c_space_id = 'crypto' AND c_dataset_id = 'bars-1m'`).Error)
				require.NoError(t, db.Exec(`DELETE FROM t_collector_timer_period_batches WHERE c_space_id = 'crypto' AND c_dataset_id = 'bars-1m'`).Error)
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
				DBPath:  dbPath,
				SpaceID: "crypto",
			})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["period_snapshot_inconsistent"]+report.UnknownStateRows["period_keys.frequency"])
		})
	}
}

func TestCollectorPeriodInventoryRejectsInvalidTimerManifestGroups(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`UPDATE t_collector_timer_period_batches SET c_group_id = 0, c_group_count = 1
		WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
}

func TestCollectorPeriodInventoryRejectsMixedTimerManifestOwners(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "first run differs", query: `UPDATE t_collector_timer_period_batches SET c_first_run_id = 'run-other' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`},
		{name: "task owner differs but target matches", query: `INSERT INTO t_collector_tasks (c_space_id,c_task_id,c_task_name,c_enabled) VALUES ('crypto','task-other','task-other',1); UPDATE t_collector_timer_period_batches SET c_task_id = 'task-other' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'; UPDATE t_collector_instance_write_targets SET c_task_id = 'task-other' WHERE c_space_id = 'crypto' AND c_write_target_id = 'timer-target-b'; UPDATE t_collector_fetch_batches SET c_request_json = replace(c_request_json, '"task_id":"task-a"', '"task_id":"task-other"') WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "route differs but request matches", query: `UPDATE t_collector_timer_period_batches SET c_route_version = 'v2' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'; UPDATE t_collector_fetch_batches SET c_request_json = replace(c_request_json, '"route_version":"v1"', '"route_version":"v2"') WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			for _, statement := range strings.Split(test.query, "; ") {
				require.NoError(t, db.Exec(statement).Error)
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryRejectsMissingTimerMembership(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "batch item", query: `DELETE FROM t_collector_fetch_batch_items WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "write target", query: `DELETE FROM t_collector_instance_write_targets WHERE c_space_id = 'crypto' AND c_write_target_id = 'timer-target-b'`},
		{name: "target series identity", query: `UPDATE t_collector_instance_write_targets SET c_series_hash = 'wrong' WHERE c_space_id = 'crypto' AND c_write_target_id = 'timer-target-b'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			require.NoError(t, db.Exec(test.query).Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryRejectsCompletionBatchIdentityMismatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		query string
	}{
		{name: "schedule", query: `UPDATE t_collector_fetch_batches SET c_schedule_id = 'other-schedule' WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "frequency", query: `UPDATE t_collector_fetch_batches SET c_frequency = '5m' WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "shard", query: `UPDATE t_collector_fetch_batches SET c_shard_index = 2 WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "planned count", query: `UPDATE t_collector_fetch_batches SET c_planned_count = 2 WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "initial attempt", query: `UPDATE t_collector_fetch_batches SET c_attempt = 2 WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "manifest deadline", query: `UPDATE t_collector_timer_period_batches SET c_deadline_at = '' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`},
		{name: "batch deadline", query: `UPDATE t_collector_fetch_batches SET c_deadline_at = '' WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`},
		{name: "first run empty", query: `UPDATE t_collector_timer_period_batches SET c_first_run_id = '' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`},
		{name: "route version empty", query: `UPDATE t_collector_timer_period_batches SET c_route_version = '' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`},
		{name: "task owner empty", query: `UPDATE t_collector_timer_period_batches SET c_task_id = '' WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			require.NoError(t, db.Exec(test.query).Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryRejectsIncompleteStorageStateIdentity(t *testing.T) {
	for _, column := range []string{"c_deadline_at", "c_confirmed_at"} {
		t.Run(column, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			require.NoError(t, db.Exec("UPDATE t_collector_period_storage_states SET "+column+" = '' WHERE c_space_id = 'crypto'").Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["period_storage_state_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryRequiresTimerDeadlineToMatchStorage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`UPDATE t_collector_timer_period_batches SET c_deadline_at = '2026-10-01 00:02:00'
		WHERE c_space_id = 'crypto' AND c_key = 'timer-b'`).Error)
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_deadline_at = '2026-10-01 00:02:00'
		WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
}

func TestCollectorPeriodInventoryRejectsIncompleteTimerSeriesCoverage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	requestJSON := inventoryTimerRequestJSON(t, "33afd910109fb556f625e05aea9f8e50", "timer-instance-b", "private-subject-marker-a",
		"timer-target-b", 1, 1, 0, inventoryFixtureSeriesHash(), "")
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_request_json = ?
		WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`, requestJSON).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
}

func TestCollectorPeriodInventoryRejectsTimerSubjectMismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	requestJSON := inventoryTimerRequestJSON(t, "33afd910109fb556f625e05aea9f8e50", "timer-instance-b", "not-the-frozen-subject",
		"timer-target-b", 1, 1, 1, inventoryFixtureSeriesHash(), "")
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_request_json = ?
		WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`, requestJSON).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
}

func TestCollectorPeriodInventoryRejectsIncompleteTimerRuntimeIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		old, new string
	}{
		{name: "missing item symbol", old: `"symbol":"private-symbol-marker"`, new: `"symbol":""`},
		{name: "missing target task", old: `"task_id":"task-a"`, new: `"task_id":""`},
		{name: "target task differs from manifest owner", old: `"task_id":"task-a"`, new: `"task_id":"task-b"`},
		{name: "missing request function", old: `"function_name":"market_data"`, new: `"function_name":""`},
		{name: "missing request node", old: `"node_id":"node-a"`, new: `"node_id":""`},
		{name: "missing request region", old: `"region":"region-a"`, new: `"region":""`},
		{name: "route version differs from manifest", old: `"route_version":"v1"`, new: `"route_version":"v2"`},
		{name: "unknown nested item field", old: `"expected_count":2`, new: `"expected_count":2,"runtime_extra":true`},
		{name: "request runtime rejects SQL-formatted item time", old: `"target_data_time":"2026-10-01T00:00:00Z"`, new: `"target_data_time":"2026-10-01 00:00:00"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			requestJSON := inventoryTimerRequestJSON(t, "33afd910109fb556f625e05aea9f8e50", "timer-instance-b", "private-subject-marker-b",
				"timer-target-b", 1, 1, 1, inventoryFixtureSeriesHash(), "")
			invalidRequest := strings.Replace(requestJSON, test.old, test.new, 1)
			require.NotEqual(t, requestJSON, invalidRequest)
			require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_request_json = ?
				WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`, invalidRequest).Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
	t.Run("unknown top-level request field", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "collector.db")
		db := openCollectorPeriodInventoryFixture(t, dbPath)
		requestJSON := inventoryTimerRequestJSON(t, "33afd910109fb556f625e05aea9f8e50", "timer-instance-b", "private-subject-marker-b",
			"timer-target-b", 1, 1, 1, inventoryFixtureSeriesHash(), "")
		invalidRequest := strings.TrimSuffix(requestJSON, "}") + `,"runtime_extra":true}`
		require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_request_json = ?
			WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`, invalidRequest).Error)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())

		report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
		require.Error(t, err)
		require.False(t, report.Complete)
		require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
	})
}

func TestDecodeCollectorPeriodInventoryTimerRequestRejectsOversizedJSON(t *testing.T) {
	raw := []byte(`{}` + strings.Repeat(" ", collectorPeriodInventoryMaxTimerRequestBytes))
	_, err := decodeCollectorPeriodInventoryTimerRequest(raw)
	require.Error(t, err)
	require.ErrorContains(t, err, "exceeds")
}

func TestCollectorPeriodInventoryRejectsTamperedTimerBindingAndInitialIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*collectorPeriodInventoryTimerRequest)
	}{
		{name: "effective provider", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Provider = "eastmoney"
			request.Items[0].Provider = "eastmoney"
		}},
		{name: "effective source", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.SourceID = "other-source"
			request.Items[0].SourceID = "other-source"
		}},
		{name: "market id", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.MarketID = "other-market"
			request.Items[0].MarketID = "other-market"
		}},
		{name: "instrument type", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.InstrumentType = "swap"
			request.Items[0].InstrumentType = "swap"
		}},
		{name: "output fields", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Items[0].OutputFields = []string{"close"}
		}},
		{name: "target output fields", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Targets[0].OutputFields = `["unsupported"]`
		}},
		{name: "item market differs from request", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Items[0].MarketID = "other-market"
		}},
		{name: "item instrument differs from request", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Items[0].InstrumentType = "swap"
		}},
		{name: "wrong sync point", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.SyncPointID = "other-sync-point"
		}},
		{name: "initial item source event", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Items[0].SourceEventID = "retry-key"
		}},
		{name: "initial item candidate index", mutate: func(request *collectorPeriodInventoryTimerRequest) {
			request.Items[0].CandidateIndex = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			mutateInventoryTimerRequest(t, db, "33afd910109fb556f625e05aea9f8e50", test.mutate)
			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryRejectsInitialTimerRetryBatchMetadata(t *testing.T) {
	for _, column := range []string{"c_parent_batch_id", "c_instance_id", "c_write_target_id", "c_retry_scope"} {
		t.Run(column, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			require.NoError(t, db.Exec("UPDATE t_collector_fetch_batches SET "+column+" = 'unexpected' WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'").Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryBindsClaimMetadataToBatchLifecycle(t *testing.T) {
	for _, test := range []struct {
		name  string
		query []string
	}{
		{name: "unclaimed but dispatched", query: []string{`UPDATE t_collector_fetch_batches SET c_status='dispatched',c_request_id='unexpected',c_dispatched_at='2026-10-01 00:00:00' WHERE c_space_id='crypto' AND c_batch_id='33afd910109fb556f625e05aea9f8e50'`}},
		{name: "claimed but planned", query: []string{`UPDATE t_collector_fetch_batches SET c_status='planned' WHERE c_space_id='crypto' AND c_batch_id='f0752ab2de364566b5d8161a48314c5f'`}},
		{name: "claimed without dispatch time", query: []string{`UPDATE t_collector_fetch_batches SET c_dispatched_at=NULL WHERE c_space_id='crypto' AND c_batch_id='f0752ab2de364566b5d8161a48314c5f'`}},
		{name: "invalid claim time", query: []string{`UPDATE t_collector_timer_period_batches SET c_claimed_at='' WHERE c_space_id='crypto' AND c_key='timer-a'`}},
		{name: "claim and dispatch times differ", query: []string{
			`UPDATE t_collector_timer_period_batches SET c_claimed_at='2026-10-01 00:00:01' WHERE c_space_id='crypto' AND c_key='timer-a'`,
			`UPDATE t_collector_fetch_batches SET c_dispatched_at='2026-10-01 00:00:02' WHERE c_space_id='crypto' AND c_batch_id='f0752ab2de364566b5d8161a48314c5f'`,
		}},
		{name: "claim after period deadline", query: []string{
			`UPDATE t_collector_timer_period_batches SET c_claimed_at='2026-10-01 00:02:00' WHERE c_space_id='crypto' AND c_key='timer-a'`,
			`UPDATE t_collector_fetch_batches SET c_dispatched_at='2026-10-01 00:02:00' WHERE c_space_id='crypto' AND c_batch_id='f0752ab2de364566b5d8161a48314c5f'`,
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			for _, query := range test.query {
				require.NoError(t, db.Exec(query).Error)
			}
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.Error(t, err)
			require.False(t, report.Complete)
			require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
		})
	}
}

func TestCollectorPeriodInventoryRejectsMismatchedTimerTargetOutputFields(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`UPDATE t_collector_instance_write_targets SET c_output_fields_json='["unsupported"]'
		WHERE c_space_id='crypto' AND c_write_target_id='timer-target-b'`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
}

func TestCollectorPeriodInventoryRejectsMixedLogicalRouteSnapshotForTimerPeriod(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	tx := db.Begin()
	require.NoError(t, tx.Error)
	seriesKeyA := "stockcn_multi\x00stockcn_multi\x00equity\x00private-subject-marker-a\x00"
	seriesKeyB := "other_provider\x00other_source\x00equity\x00private-subject-marker-b\x00"
	digest := sha256.Sum256([]byte(seriesKeyA + "\x00" + seriesKeyB + "\x00"))
	seriesHash := hex.EncodeToString(digest[:])
	require.NoError(t, tx.Exec(`UPDATE t_collector_task_period_series SET c_provider='other_provider',c_source_id='other_source',c_series_key=?,c_series_hash=?
		WHERE c_space_id='crypto' AND c_series_index=1`, seriesKeyB, seriesHash).Error)
	require.NoError(t, tx.Exec(`UPDATE t_collector_task_period_series SET c_series_hash=? WHERE c_space_id='crypto' AND c_series_index=0`, seriesHash).Error)
	var rawPeriodTime string
	require.NoError(t, tx.Raw(`SELECT c_period_time FROM t_collector_task_period_series WHERE c_space_id='crypto' LIMIT 1`).Scan(&rawPeriodTime).Error)
	periods := []collectorPeriodInventoryPeriod{{
		DatasetID: "bars-1m", Frequency: "1m", RawPeriodTime: rawPeriodTime,
		SnapshotSeriesRows: 2, SnapshotExpectedCountMin: 2, SnapshotExpectedCountMax: 2, SnapshotHashVariants: 1,
		SnapshotSeriesHash: seriesHash, SnapshotRequired: true, SnapshotValid: true, TimerBatchCount: 2,
	}}
	_, err := validateCollectorPeriodInventorySnapshotContents(context.Background(), tx, "crypto", periods)
	require.NoError(t, err)
	require.False(t, periods[0].SnapshotValid)
	require.NoError(t, tx.Rollback().Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

func TestCollectorPeriodInventoryAcceptsLegalUnclaimedTimerTerminalStates(t *testing.T) {
	for _, status := range []string{"failed", "timed_out"} {
		t.Run(status, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "collector.db")
			db := openCollectorPeriodInventoryFixture(t, dbPath)
			require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_status=?,c_completed_at='2026-10-01 00:01:00'
				WHERE c_space_id='crypto' AND c_batch_id='33afd910109fb556f625e05aea9f8e50'`, status).Error)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			require.NoError(t, sqlDB.Close())

			report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
			require.NoError(t, err)
			require.True(t, report.Complete)
		})
	}
}

func mutateInventoryTimerRequest(t *testing.T, db *gorm.DB, batchID string, mutate func(*collectorPeriodInventoryTimerRequest)) {
	t.Helper()
	var raw string
	require.NoError(t, db.Raw(`SELECT c_request_json FROM t_collector_fetch_batches WHERE c_space_id='crypto' AND c_batch_id=?`, batchID).Scan(&raw).Error)
	request, err := decodeCollectorPeriodInventoryTimerRequest([]byte(raw))
	require.NoError(t, err)
	mutate(&request)
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_request_json=? WHERE c_space_id='crypto' AND c_batch_id=?`, string(encoded), batchID).Error)
}

func TestCollectorPeriodInventoryRejectsTimerClaimMetadataMismatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`UPDATE t_collector_timer_period_batches SET c_claimed_at = NULL
		WHERE c_space_id = 'crypto' AND c_key = 'timer-a'`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["timer_period_manifest_inconsistent"])
}

func TestCollectorPeriodInventoryRejectsUndeliverableFailureReceipt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	targets := `[{"space_id":"crypto","dataset_id":"bars-1m","series_hash":"hash-a","expected_count":1,"series_index":0}]`
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items
		(c_space_id,c_retry_key,c_source_batch_id,c_write_target_id,c_subject_id,c_frequency,c_target_data_time,c_task_json,c_failure_targets_json,c_status,c_period_failure_report_state)
		VALUES ('crypto','retry-invalid-target','batch-a','target-a','private-subject-marker','1m','2026-10-01 00:00:00','{}',?,'permanent_failed','pending')`, targets).Error)
	wrongSpaceTargets := `[{"write_target_id":"foreign-target","space_id":"other","dataset_id":"bars-1m","series_hash":"hash-b","expected_count":1,"series_index":0}]`
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items
		(c_space_id,c_retry_key,c_source_batch_id,c_write_target_id,c_subject_id,c_frequency,c_target_data_time,c_task_json,c_failure_targets_json,c_status,c_period_failure_report_state)
		VALUES ('crypto','retry-foreign-target','batch-a','target-a','private-subject-marker','1m','2026-10-01 00:00:00','{}',?,'permanent_failed','pending')`, wrongSpaceTargets).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["pending_failure_targets_invalid"])
	require.NotZero(t, report.IntegrityIssues["pending_failure_receipt_invalid"])
}

func TestCollectorPeriodInventoryRejectsMismatchedFailureReceiptResults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	invalidResults := `[{"write_target_id":"foreign-target","space_id":"crypto","dataset_id":"bars-1m","frequency":"1m","period_time":"2026-10-01T00:00:00Z","series_hash":"hash","expected_count":2,"series_index":0,"disposition":"recorded","observed_at":"2026-10-01T00:01:00Z"}]`
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_retry_items SET c_period_failure_results_json = ?
		WHERE c_space_id = 'crypto' AND c_retry_key = 'retry-a'`, invalidResults).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["pending_failure_receipt_results_invalid"])
}

func TestCollectorPeriodInventoryAcceptsPartialValidFailureReceiptResults(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_task_instances
		(c_space_id,c_instance_id,c_run_id,c_frequency,c_subject_id,c_provider_symbol)
		VALUES ('crypto','instance-b','run-a','1m','private-subject-marker-b','private-symbol-marker-b')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_instance_write_targets
		(c_space_id,c_write_target_id,c_instance_id,c_task_id,c_dataset_id,c_status)
		VALUES ('crypto','target-b','instance-b','task-a','bars-1m','permanent_failed')`).Error)
	targets := fmt.Sprintf(`[{"write_target_id":"target-a","space_id":"crypto","dataset_id":"bars-1m","series_hash":%q,"expected_count":2,"series_index":0},{"write_target_id":"target-b","space_id":"crypto","dataset_id":"bars-1m","series_hash":%q,"expected_count":2,"series_index":1}]`, inventoryFixtureSeriesHash(), inventoryFixtureSeriesHash())
	validPartialResults := fmt.Sprintf(`[{"write_target_id":"target-a","space_id":"crypto","dataset_id":"bars-1m","frequency":"1m","period_time":"2026-10-01T00:00:00Z","series_hash":%q,"expected_count":2,"series_index":0,"disposition":"recorded","observed_at":"2026-10-01T00:01:00Z"}]`, inventoryFixtureSeriesHash())
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_retry_items SET c_failure_targets_json = ?, c_period_failure_results_json = ?
		WHERE c_space_id = 'crypto' AND c_retry_key = 'retry-a'`, targets, validPartialResults).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{DBPath: dbPath, SpaceID: "crypto"})
	require.NoError(t, err)
	require.True(t, report.Complete)
}

func TestCollectorPeriodInventoryFailsClosedWhenPeriodLimitIsExceeded(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_period_storage_states
		(c_space_id,c_dataset_id,c_frequency,c_period_time,c_series_hash,c_expected_count,c_deadline_at,c_status,c_confirmed_at)
		VALUES ('crypto','bars-1m','1m','2026-10-01 00:01:00','hash-2',1,'2026-10-01 00:02:00','waiting','2026-10-01 00:01:00')`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:   dbPath,
		SpaceID:  "crypto",
		MaxItems: 1,
	})
	require.Error(t, err)
	require.True(t, report.PeriodKeysTruncated)
	require.False(t, report.Complete)
}

func TestCollectorPeriodInventoryDoesNotListCompletedRetryFailureReceipts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items
		(c_space_id,c_retry_key,c_source_batch_id,c_write_target_id,c_subject_id,c_frequency,c_target_data_time,c_status,c_period_failure_report_state)
		VALUES ('crypto','retry-succeeded','batch-a','target-a','private-subject-marker','1m','2026-10-01 00:00:00','succeeded','pending')`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	require.Len(t, report.PendingRetries, 1)
	require.Equal(t, "permanent_failed", report.PendingRetries[0].Status)
}

func TestCollectorPeriodInventoryProjectsFetchScopeFailureTargets(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	targets := `[{"write_target_id":"target-fetch-a","space_id":"crypto","dataset_id":"bars-1m","series_hash":"hash-a","expected_count":2,"series_index":0},{"write_target_id":"target-fetch-b","space_id":"crypto","dataset_id":"bars-5m","series_hash":"hash-b","expected_count":1,"series_index":0}]`
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items
		(c_space_id,c_retry_key,c_source_batch_id,c_instance_id,c_retry_scope,c_subject_id,c_frequency,c_target_data_time,c_task_json,c_failure_targets_json,c_status,c_period_failure_report_state)
		VALUES ('crypto','retry-fetch','batch-a','instance-a','fetch','private-subject-marker','1m','2026-10-01 00:00:00',?,?,'permanent_failed','pending')`, inventoryFailureTaskJSON(), targets).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	require.True(t, report.Complete)
	require.Len(t, report.PendingRetries, 2)
	require.Equal(t, []string{"bars-1m", "bars-5m"}, report.PendingRetries[1].DatasetIDs)
	require.NotContains(t, collectorPeriodInventoryJSON(t, report), "private-subject-marker")
}

func TestCollectorPeriodInventoryParsesGoSQLiteTimestampValues(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	periodTime := time.Date(2026, 10, 1, 0, 0, 0, 123456789, time.UTC)
	deadline := periodTime.Add(time.Minute)
	require.NoError(t, db.Exec(`UPDATE t_collector_period_storage_states SET c_deadline_at = ? WHERE c_space_id = 'crypto' AND c_dataset_id = 'bars-1m'`, deadline).Error)
	require.NoError(t, db.Exec(`UPDATE t_collector_timer_period_batches SET c_deadline_at = ? WHERE c_space_id = 'crypto'`, deadline).Error)
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_deadline_at = ? WHERE c_space_id = 'crypto' AND c_batch_id = '33afd910109fb556f625e05aea9f8e50'`, deadline).Error)
	var rawTime string
	require.NoError(t, db.Raw(`SELECT coalesce(c_deadline_at, '') FROM t_collector_period_storage_states WHERE c_space_id = 'crypto' AND c_dataset_id = 'bars-1m'`).Scan(&rawTime).Error)
	require.Contains(t, rawTime, "+00:00")
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	require.Len(t, report.PeriodKeys, 1)
	require.Equal(t, deadline.Format(time.RFC3339Nano), report.PeriodKeys[0].StorageDeadlineAt)
}

func TestSafeInventoryTimestampAcceptsSQLiteTimeFormats(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "2026-10-01 00:00:00.123456789+00:00", want: "2026-10-01T00:00:00.123456789Z"},
		{input: "2026-10-01 00:00:00.123456789 +0000 UTC", want: "2026-10-01T00:00:00.123456789Z"},
		{input: "2026-10-01T00:00:00.123456789Z", want: "2026-10-01T00:00:00.123456789Z"},
	}
	for _, test := range cases {
		t.Run(test.input, func(t *testing.T) {
			report := &collectorPeriodInventoryReport{UnknownStateRows: map[string]int64{}}
			require.Equal(t, test.want, safeInventoryTimestamp(test.input, "timestamp", report))
			require.Empty(t, report.UnknownStateRows)
		})
	}
}

func TestCollectorPeriodInventoryOpensLiteralPercentEncodedPath(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "collector%3Fdata.db")
	decodedPath := filepath.Join(dir, "collector?data.db")
	targetDB := openCollectorPeriodInventoryFixture(t, targetPath)
	targetSQLDB, err := targetDB.DB()
	require.NoError(t, err)
	require.NoError(t, targetSQLDB.Close())
	decodedDB := openCollectorPeriodInventoryFixture(t, decodedPath)
	require.NoError(t, decodedDB.Exec(`INSERT INTO t_collector_tasks (c_space_id,c_task_id,c_task_name,c_enabled) VALUES ('crypto','extra-task','extra-task',1)`).Error)
	decodedSQLDB, err := decodedDB.DB()
	require.NoError(t, err)
	require.NoError(t, decodedSQLDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  targetPath,
		SpaceID: "crypto",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, report.TableCounts["t_collector_tasks"], "must read the exact file checked by Lstat")
}

func TestCollectorPeriodInventoryFailsClosedOnInconsistentSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`UPDATE t_collector_task_period_series SET c_expected_count = 3 WHERE c_space_id = 'crypto' AND c_series_index = 0`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.NotZero(t, report.IntegrityIssues["period_snapshot_inconsistent"])
}

func TestCollectorPeriodInventoryFailsClosedWhenStorageHashDiffersFromSnapshot(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`UPDATE t_collector_period_storage_states SET c_series_hash = 'different-hash' WHERE c_space_id = 'crypto'`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.False(t, report.PeriodKeys[0].SnapshotValid)
}

func TestCollectorPeriodInventoryLabelsGlobalReadinessOrphans(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = OFF`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_period_readiness_items (c_readiness_id,c_instance_id,c_write_target_id,c_subject_id,c_state,c_updated_at) VALUES (999999,'instance-x','target-x','private-subject-marker','pending','2026-10-01 00:00:00')`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.EqualValues(t, 1, report.GlobalIntegrityIssues["orphan_readiness_items"])
}

func collectorPeriodInventoryJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func TestCollectorPeriodInventoryBucketsUnknownStatesWithoutEchoingThem(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	db := openCollectorPeriodInventoryFixture(t, dbPath)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_batches
		(c_space_id,c_batch_id,c_schedule_id,c_batch_kind,c_shard_index,c_frequency,c_region,c_node_id,c_function_name,c_status)
		VALUES ('crypto','batch-unknown','schedule-unknown','realtime',1,'1m','region-a','node-a','function-a','private-subject-marker')`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	report, err := runCollectorPeriodInventory(context.Background(), collectorPeriodInventoryFlags{
		DBPath:  dbPath,
		SpaceID: "crypto",
	})
	require.Error(t, err)
	require.False(t, report.Complete)
	require.EqualValues(t, 1, report.UnknownStateRows["fetch_batches.status"])
	require.EqualValues(t, 1, report.StateCounts["fetch_batches.status"]["unknown"])
	encoded, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-subject-marker")
}

func openCollectorPeriodInventoryFixture(t *testing.T, dbPath string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	_, sourceFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../../.."))
	schemaSQL, err := os.ReadFile(filepath.Join(repoRoot, "modules/collector/schema/collector.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(schemaSQL)).Error)
	seedCollectorPeriodInventoryFixture(t, db)
	return db
}

func seedCollectorPeriodInventoryFixture(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO t_collector_tasks (c_space_id,c_task_id,c_task_name,c_enabled) VALUES ('crypto','task-a','task-a',1)`,
		`INSERT INTO t_collector_task_instances (c_space_id,c_instance_id,c_run_id,c_frequency,c_subject_id,c_provider_symbol) VALUES ('crypto','instance-a','run-a','1m','private-subject-marker','private-symbol-marker')`,
		`INSERT INTO t_collector_instance_write_targets (c_space_id,c_write_target_id,c_instance_id,c_task_id,c_dataset_id,c_status) VALUES ('crypto','target-a','instance-a','task-a','bars-1m','pending')`,
		`INSERT INTO t_collector_runs (c_space_id,c_run_id,c_run_key,c_status,c_frequency,c_target_time) VALUES ('crypto','run-a','run-key-a','planned','1m','2026-10-01 00:00:00')`,
		`INSERT INTO t_collector_fetch_batches (c_space_id,c_batch_id,c_schedule_id,c_batch_kind,c_shard_index,c_frequency,c_region,c_node_id,c_function_name,c_status,c_deadline_at) VALUES ('crypto','batch-a','schedule-a','realtime',0,'1m','region-a','node-a','function-a','dispatched','2026-10-01 00:01:00')`,
		`INSERT INTO t_collector_fetch_batch_items (c_space_id,c_batch_id,c_instance_id,c_status) VALUES ('crypto','batch-a','instance-a','pending')`,
		`INSERT INTO t_period_readiness (c_space_id,c_dataset_id,c_frequency,c_work_type,c_period_time,c_deadline_at,c_status,c_report_state) VALUES ('crypto','bars-1m','1m','collection','2026-10-01 00:00:00','2026-10-01 00:01:00','waiting','pending')`,
		`INSERT INTO t_period_readiness_items (c_readiness_id,c_instance_id,c_write_target_id,c_subject_id,c_state,c_updated_at) VALUES (1,'instance-a','target-a','private-subject-marker','pending','2026-10-01 00:00:00')`,
		`INSERT INTO t_collector_tasks (c_space_id,c_task_id,c_task_name,c_enabled) VALUES ('other','task-b','task-b',1)`,
		`INSERT INTO t_collector_period_storage_states (c_space_id,c_dataset_id,c_frequency,c_period_time,c_series_hash,c_expected_count,c_deadline_at,c_status,c_confirmed_at) VALUES ('other','bars-1m','1m','2026-10-01 00:00:00','other-hash',1,'2026-10-01 00:01:00','waiting','2026-10-01 00:00:00')`,
	}
	for _, statement := range statements {
		require.NoError(t, db.Exec(statement).Error)
	}
	seriesKeyA := "stockcn_multi\x00stockcn_multi\x00equity\x00private-subject-marker-a\x00"
	seriesKeyB := "stockcn_multi\x00stockcn_multi\x00equity\x00private-subject-marker-b\x00"
	seriesHash := inventoryFixtureSeriesHash()
	targetJSON := fmt.Sprintf(`[{"write_target_id":"target-a","space_id":"crypto","dataset_id":"bars-1m","series_hash":%q,"expected_count":2,"series_index":0}]`, seriesHash)
	taskJSON := inventoryFailureTaskJSON()
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items
		(c_space_id,c_retry_key,c_source_batch_id,c_write_target_id,c_subject_id,c_frequency,c_target_data_time,c_task_json,c_failure_targets_json,c_status,c_period_failure_report_state)
		VALUES ('crypto','retry-a','batch-a','target-a','private-subject-marker','1m','2026-10-01 00:00:00',?,?,'permanent_failed','pending')`, taskJSON, targetJSON).Error)
	for index, entry := range []struct{ seriesKey, subject string }{{seriesKeyA, "private-subject-marker-a"}, {seriesKeyB, "private-subject-marker-b"}} {
		require.NoError(t, db.Exec(`INSERT INTO t_collector_task_period_series
			(c_space_id,c_dataset_id,c_frequency,c_period_time,c_series_index,c_series_key,c_subject_id,c_provider,c_source_id,c_market_type,c_provider_symbol,c_series_hash,c_expected_count)
			VALUES ('crypto','bars-1m','1m','2026-10-01 00:00:00',?,?,?,?,?,'equity','private-symbol-marker',?,2)`, index, entry.seriesKey, entry.subject, "stockcn_multi", "stockcn_multi", seriesHash).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO t_collector_period_storage_states
		(c_space_id,c_dataset_id,c_frequency,c_period_time,c_series_hash,c_expected_count,c_deadline_at,c_status,c_confirmed_at)
		VALUES ('crypto','bars-1m','1m','2026-10-01 00:00:00',?,2,'2026-10-01 00:01:00','waiting','2026-10-01 00:00:00')`, seriesHash).Error)
	for _, timerBatch := range []struct {
		batchID, instanceID, subjectID, targetID, status, requestID string
		groupID, shardIndex, seriesIndex                            int
	}{
		{batchID: "f0752ab2de364566b5d8161a48314c5f", instanceID: "timer-instance-a", subjectID: "private-subject-marker-a", targetID: "timer-target-a", status: "dispatched", requestID: "request-a", groupID: 0, shardIndex: 0, seriesIndex: 0},
		{batchID: "33afd910109fb556f625e05aea9f8e50", instanceID: "timer-instance-b", subjectID: "private-subject-marker-b", targetID: "timer-target-b", status: "planned", groupID: 1, shardIndex: 1, seriesIndex: 1},
	} {
		requestJSON := inventoryTimerRequestJSON(t, timerBatch.batchID, timerBatch.instanceID, timerBatch.subjectID,
			timerBatch.targetID, timerBatch.groupID, timerBatch.shardIndex, timerBatch.seriesIndex, seriesHash, timerBatch.requestID)
		periodTime := "2026-10-01T00:00:00Z"
		scheduleID := "timer:" + collectorPeriodInventoryStableID("crypto", "bars-1m", "1m", periodTime, fmt.Sprint(timerBatch.shardIndex))
		var dispatchedAt any
		if timerBatch.status == "dispatched" {
			dispatchedAt = "2026-10-01 00:00:00"
		}
		require.NoError(t, db.Exec(`INSERT INTO t_collector_task_instances
			(c_space_id,c_instance_id,c_run_id,c_provider,c_provider_symbol,c_market_type,c_subject_id,c_frequency)
			VALUES ('crypto',?,'run-a','stockcn_multi','private-symbol-marker','equity',?,'1m')`,
			timerBatch.instanceID, timerBatch.subjectID).Error)
		require.NoError(t, db.Exec(`INSERT INTO t_collector_instance_write_targets
			(c_space_id,c_write_target_id,c_instance_id,c_task_id,c_dataset_id,c_series_index,c_series_hash,c_expected_count)
			VALUES ('crypto',?,?,'task-a','bars-1m',?,?,2)`, timerBatch.targetID, timerBatch.instanceID, timerBatch.seriesIndex, seriesHash).Error)
		require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_batches
			(c_space_id,c_batch_id,c_schedule_id,c_batch_kind,c_shard_index,c_frequency,c_region,c_node_id,c_function_name,c_request_id,c_status,c_request_json,c_planned_count,c_dispatched_at,c_deadline_at)
			VALUES ('crypto',?,?,'realtime',?,'1m','region-a','node-a','market_data',?,?,?,?,?,'2026-10-01 00:01:00')`,
			timerBatch.batchID, scheduleID, timerBatch.shardIndex, timerBatch.requestID, timerBatch.status, requestJSON, 1, dispatchedAt).Error)
		require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_batch_items (c_space_id,c_batch_id,c_instance_id)
			VALUES ('crypto',?,?)`, timerBatch.batchID, timerBatch.instanceID).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO t_collector_timer_period_batches
		(c_key,c_space_id,c_dataset_id,c_frequency,c_period_time,c_task_id,c_first_run_id,c_series_hash,c_expected_count,c_group_id,c_group_count,c_shard_index,c_binding_hash,c_route_version,c_batch_id,c_function_name,c_node_id,c_region,c_claim_request_id,c_claimed_at,c_deadline_at)
		VALUES ('timer-a','crypto','bars-1m','1m','2026-10-01 00:00:00','task-a','run-a',?,2,0,2,0,
		(SELECT json_extract(c_request_json,'$.binding_hash') FROM t_collector_fetch_batches WHERE c_space_id='crypto' AND c_batch_id='f0752ab2de364566b5d8161a48314c5f'),
		'v1','f0752ab2de364566b5d8161a48314c5f','market_data','node-a','region-a','request-a','2026-10-01 00:00:00','2026-10-01 00:01:00')`, seriesHash).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_timer_period_batches
		(c_key,c_space_id,c_dataset_id,c_frequency,c_period_time,c_task_id,c_first_run_id,c_series_hash,c_expected_count,c_group_id,c_group_count,c_shard_index,c_binding_hash,c_route_version,c_batch_id,c_function_name,c_node_id,c_region,c_claim_request_id,c_deadline_at)
		VALUES ('timer-b','crypto','bars-1m','1m','2026-10-01 00:00:00','task-a','run-a',?,2,1,2,1,
		(SELECT json_extract(c_request_json,'$.binding_hash') FROM t_collector_fetch_batches WHERE c_space_id='crypto' AND c_batch_id='33afd910109fb556f625e05aea9f8e50'),
		'v1','33afd910109fb556f625e05aea9f8e50','market_data','node-a','region-a','','2026-10-01 00:01:00')`, seriesHash).Error)
	// Claim replaces the batch deadline with an execution timeout; the manifest retains the Storage deadline.
	require.NoError(t, db.Exec(`UPDATE t_collector_fetch_batches SET c_deadline_at = '2026-10-01 00:02:00'
		WHERE c_space_id = 'crypto' AND c_batch_id = 'f0752ab2de364566b5d8161a48314c5f'`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_task_period_series
		(c_space_id,c_dataset_id,c_frequency,c_period_time,c_series_index,c_series_key,c_subject_id,c_provider,c_source_id,c_market_type,c_provider_symbol,c_series_hash,c_expected_count)
		VALUES ('other','bars-1m','1m','2026-10-01 00:00:00',0,'other-series','other-private-subject','provider','source','spot','other-private-symbol','other-hash',99)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_timer_period_batches
		(c_key,c_space_id,c_dataset_id,c_frequency,c_period_time,c_task_id,c_first_run_id,c_series_hash,c_expected_count,c_group_id,c_group_count,c_shard_index,c_binding_hash,c_route_version,c_batch_id,c_function_name,c_node_id,c_region,c_claim_request_id,c_deadline_at)
		VALUES ('other-timer','other','bars-1m','1m','2026-10-01 00:00:00','task-b','run-b','other-hash',99,0,1,0,'binding','v1','other-timer-batch','market_data','node-b','region-b','other-request','2026-10-01 00:01:00')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_period_readiness (c_space_id,c_dataset_id,c_frequency,c_work_type,c_period_time,c_deadline_at,c_status,c_report_state)
		VALUES ('other','bars-1m','1m','collection','2026-10-01 00:00:00','2026-10-01 00:01:00','waiting','pending')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_period_readiness_items (c_readiness_id,c_instance_id,c_write_target_id,c_subject_id,c_state,c_updated_at)
		VALUES (2,'other-instance','other-target','other-private-subject','pending','2026-10-01 00:00:00')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO t_collector_fetch_retry_items
		(c_space_id,c_retry_key,c_source_batch_id,c_write_target_id,c_subject_id,c_frequency,c_target_data_time,c_failure_targets_json,c_status,c_period_failure_report_state)
		VALUES ('other','other-retry','other-batch','other-target','other-private-subject','1m','2026-10-01 00:00:00','[]','permanent_failed','pending')`).Error)
}

func inventoryFixtureSeriesHash() string {
	seriesKeyA := "stockcn_multi\x00stockcn_multi\x00equity\x00private-subject-marker-a\x00"
	seriesKeyB := "stockcn_multi\x00stockcn_multi\x00equity\x00private-subject-marker-b\x00"
	digest := sha256.Sum256([]byte(seriesKeyA + "\x00" + seriesKeyB + "\x00"))
	return hex.EncodeToString(digest[:])
}

func inventoryFailureTaskJSON() string {
	return `{"market_type":"spot","frequency":"1m","target_data_time":"2026-10-01T00:00:00Z"}`
}

func inventoryTimerRequestJSON(t *testing.T, batchID, instanceID, subjectID, targetID string, groupID, shardIndex, seriesIndex int, seriesHash, requestID string) string {
	t.Helper()
	period := "2026-10-01T00:00:00Z"
	item := collectorPeriodInventoryTimerItem{
		InstanceID: instanceID, SubjectID: subjectID, Symbol: "private-symbol-marker", Provider: "sina", SourceID: "stockcn_minute_http",
		MarketID: "stockcn", InstrumentType: "equity", MarketType: "equity", DataType: "kline",
		TargetDataTime: period, DatasetID: "bars-1m", Frequency: "1m",
		RequirePeriodCommit: true, SeriesIndex: uint32(seriesIndex), SeriesHash: seriesHash, ExpectedCount: 2,
	}
	shard := fmt.Sprint(shardIndex)
	scheduleID := "timer:" + collectorPeriodInventoryStableID("crypto", "bars-1m", "1m", period, shard)
	syncPointID := collectorPeriodInventoryStableID("crypto", "bars-1m", "1m", period, shard, "timer-sync-point")
	bindingHash := collectorPeriodInventoryTimerBindingHash("stockcn_multi", "sina", "stockcn_minute_http", "v1",
		groupID, 2, "equity", "stockcn", "equity", "bars-1m", "1m", strings.Join(item.OutputFields, ","),
		"node-a", "market_data", "region-a")
	target := collectorPeriodInventoryTimerTarget{
		WriteTargetID: targetID, SpaceID: "crypto", InstanceID: instanceID, TaskID: "task-a", DatasetID: "bars-1m", Frequency: "1m",
		OutputFields: "[]", TargetDataTime: period, SeriesIndex: uint32(seriesIndex), SeriesHash: seriesHash, ExpectedCount: 2, Status: "pending",
	}
	request := collectorPeriodInventoryTimerRequest{
		BatchID: batchID, SyncPointID: syncPointID, ScheduleID: scheduleID, BatchKind: "realtime", SpaceID: "crypto", MarketID: "stockcn", InstrumentType: "equity",
		DatasetID: "bars-1m", Frequency: "1m", Provider: "sina", SourceID: "stockcn_minute_http", MarketType: "equity", NodeID: "node-a", Region: "region-a",
		FunctionName: "market_data", RequestID: requestID,
		GroupID: groupID, GroupCount: 2, ShardIndex: shardIndex, BindingHash: bindingHash, RouteVersion: "v1", RequirePeriodCommit: true,
		Items: []collectorPeriodInventoryTimerItem{item}, Targets: []collectorPeriodInventoryTimerTarget{target},
	}
	encoded, err := json.Marshal(request)
	require.NoError(t, err)
	return string(encoded)
}

func snapshotInventoryFixtureFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "-shm") {
			result[entry.Name()] = "present"
			continue
		}
		if !strings.HasSuffix(entry.Name(), "-wal") && !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		digest := sha256.Sum256(contents)
		result[entry.Name()] = hex.EncodeToString(digest[:])
	}
	return result
}
