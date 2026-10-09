package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	require.NotNil(t, mgr.PeriodSeriesSnapshot())
	var absent *Store
	require.Nil(t, absent.PeriodSeriesSnapshot())
	var count int64
	if err := mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 't_collector_%'`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Open() created %d collector tables, want 0", count)
	}
}

func TestApplySchemaCreatesCurrentTaskAndInstanceTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	require.Equal(t, "t_collector_task_period_series", (&domain.PeriodSeriesSnapshotEntry{}).TableName())
	for _, table := range []string{"t_collector_tasks", "t_collector_task_tags", "t_collector_task_series", "t_collector_task_period_series", "t_collector_period_storage_states", "t_collector_timer_period_batches", "t_collector_runs", "t_collector_task_instances", "t_collector_instance_write_targets", "t_collector_fetch_batches", "t_collector_fetch_batch_items", "t_collector_fetch_retry_items", "t_period_readiness", "t_period_readiness_items"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error; err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
	for table, columns := range map[string][]string{
		"t_collector_tasks":                  {"c_definition_hash", "c_series_hash", "c_result_dataset_id", "c_result_view_id"},
		"t_collector_task_tags":              {"c_task_id", "c_tag_id"},
		"t_collector_task_series":            {"c_task_id", "c_series_index", "c_series_key", "c_subject_id", "c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag"},
		"t_collector_task_period_series":     {"c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_series_index", "c_series_key", "c_subject_id", "c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag", "c_series_hash", "c_expected_count", "c_ctime"},
		"t_collector_period_storage_states":  {"c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_series_hash", "c_expected_count", "c_deadline_at", "c_status", "c_confirmed_at"},
		"t_collector_timer_period_batches":   {"c_key", "c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_task_id", "c_first_run_id", "c_series_hash", "c_expected_count", "c_group_id", "c_group_count", "c_shard_index", "c_binding_hash", "c_route_version", "c_batch_id", "c_function_name", "c_node_id", "c_region", "c_claim_request_id", "c_claimed_at", "c_deadline_at", "c_ctime"},
		"t_collector_runs":                   {"c_run_id", "c_run_key", "c_run_type"},
		"t_collector_task_instances":         {"c_instance_id", "c_run_id", "c_request_key", "c_source_id", "c_series_tag"},
		"t_collector_instance_write_targets": {"c_write_target_id", "c_instance_id", "c_task_id", "c_dataset_id", "c_series_index", "c_series_hash", "c_expected_count"},
		"t_collector_fetch_batches":          {"c_instance_id", "c_write_target_id", "c_retry_scope", "c_period_time", "c_period_deadline_at", "c_items_cleaned"},
		"t_collector_fetch_batch_items":      {"c_batch_id", "c_instance_id"},
		"t_collector_fetch_retry_items":      {"c_instance_id", "c_write_target_id", "c_retry_scope", "c_period_time", "c_period_deadline_at", "c_failure_targets_json", "c_period_failure_report_state", "c_period_failure_results_json", "c_period_failure_last_error", "c_period_failure_deadline_exceeded_at"},
		"t_period_readiness_items":           {"c_write_target_id", "c_series_tag"},
	} {
		for _, column := range columns {
			var count int64
			if err := mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count).Error; err != nil {
				t.Fatalf("query column %s.%s: %v", table, column, err)
			}
			if count != 1 {
				t.Fatalf("column %s.%s count = %d, want 1", table, column, count)
			}
		}
	}
	for _, index := range []string{"idx_collector_instances_run", "idx_collector_instances_space_page", "idx_collector_instances_list_page", "idx_collector_instances_space_status_page", "idx_collector_instances_storage_write", "idx_collector_instances_terminal_cleanup_space", "idx_collector_batch_items_instance_batch", "idx_collector_task_period_series_lookup", "idx_collector_task_period_series_key", "idx_collector_task_period_series_retention", "idx_collector_task_period_series_space_retention", "idx_collector_period_storage_terminal_cleanup", "idx_collector_period_storage_waiting_cursor", "idx_collector_runs_terminal_cleanup", "idx_collector_runs_terminal_cleanup_space", "idx_collector_write_targets_retention_space", "idx_collector_fetch_batch_instance_ref", "idx_collector_fetch_batch_target_ref", "idx_collector_fetch_batch_period_due", "idx_collector_fetch_batch_terminal_items_cleanup", "idx_collector_fetch_batch_terminal_items_cleanup_space", "idx_collector_fetch_batch_terminal_parent_cleanup", "idx_collector_fetch_batch_terminal_parent_cleanup_space", "idx_collector_fetch_retry_instance_active", "idx_collector_retry_period_due", "idx_collector_fetch_retry_failure_reports", "idx_collector_fetch_retry_cleanup_succeeded_space", "idx_collector_fetch_retry_cleanup_permanent_space", "idx_collector_timer_period_batches_claim_request", "idx_collector_timer_period_batches_candidate", "idx_period_readiness_items_write_target"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&count).Error; err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
	require.Error(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (c_space_id, c_retry_key, c_source_batch_id, c_subject_id, c_frequency, c_target_data_time, c_status, c_period_failure_report_state) VALUES ('crypto', 'invalid', '', 'ETH-USDT', '1m', '2026-09-30T09:10:00Z', 'permanent_failed', 'unknown')`).Error)
	if _, _, err := mgr.TaskInstances().List(context.Background(), TaskInstanceFilter{Page: 1, PageSize: 1}); err != nil {
		t.Fatalf("query current task instances: %v", err)
	}
	for _, forbidden := range []string{"t_collector_run_tasks", "t_collector_run_series"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", forbidden).Scan(&count).Error; err != nil {
			t.Fatalf("query forbidden snapshot table %s: %v", forbidden, err)
		}
		if count != 0 {
			t.Fatalf("forbidden per-run snapshot table %s exists", forbidden)
		}
	}
}

func TestPeriodSeriesSchemaEnforcesPeriodIndexAndKeyUniqueness(t *testing.T) {
	s := newCollectorStore(t)
	insert := `INSERT INTO t_collector_task_period_series (
		c_space_id, c_dataset_id, c_frequency, c_period_time, c_series_index, c_series_key,
		c_subject_id, c_provider, c_source_id, c_market_type, c_provider_symbol, c_series_tag,
		c_series_hash, c_expected_count
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	args := []any{"crypto", "bars", "1m", time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), 0, "series-a", "BTC-USDT", "binance", "spot_http", "spot", "BTCUSDT", "venue:binance", "hash", 2}
	require.NoError(t, s.db.Exec(insert, args...).Error)

	duplicateIndex := append([]any(nil), args...)
	duplicateIndex[5] = "series-b"
	require.Error(t, s.db.Exec(insert, duplicateIndex...).Error, "one period cannot assign the same index twice")

	duplicateKey := append([]any(nil), args...)
	duplicateKey[4] = 1
	require.Error(t, s.db.Exec(insert, duplicateKey...).Error, "one series key cannot occur twice in a period")
}

func TestDeleteTaskRuntimeRemovesOnlyOwnedWriteTargets(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", Enabled: true},
	} {
		require.NoError(t, mgr.Tasks().Create(ctx, task))
	}
	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "shared", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m"}
	require.NoError(t, mgr.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, mgr.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-a", DatasetID: "bars-a", Status: "pending"},
		{ID: "target-b", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-b", DatasetID: "bars-b", Status: "pending"},
	}))
	pendingTargets, err := json.Marshal([]domain.WriteTarget{{ID: "target-a", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-a", DatasetID: "bars-a", SeriesHash: "hash-a", ExpectedCount: 1}})
	require.NoError(t, err)
	settledTargets, err := json.Marshal([]domain.WriteTarget{{ID: "target-a", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-a", DatasetID: "bars-a", SeriesHash: "hash-a", ExpectedCount: 1}})
	require.NoError(t, err)
	require.NoError(t, mgr.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "pending-period-failure", WriteTargetID: "target-a", Frequency: "1m",
		FailureTargetsJSON: string(pendingTargets), Status: "permanent_failed", PeriodFailureReportState: domain.PeriodFailureReportPending,
	}))
	require.NoError(t, mgr.FetchRetries().Upsert(ctx, &domain.RetryItem{
		SpaceID: "crypto", RetryKey: "settled-period-failure", WriteTargetID: "target-a", Frequency: "1m",
		FailureTargetsJSON: string(settledTargets), Status: "permanent_failed", PeriodFailureReportState: domain.PeriodFailureReportAcknowledged,
	}))
	require.NoError(t, mgr.FetchRetries().DeleteByTaskID(ctx, "crypto", "task-a"))
	period := time.Now().UTC().Truncate(time.Minute)
	_, err = mgr.PeriodReadiness().EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars-a", Frequency: "1m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute),
		Tasks:      []domain.PeriodTaskSeed{{InstanceID: "shared", WriteTargetID: "target-a", SubjectID: "BTC-USDT"}},
	})
	require.NoError(t, err)

	require.NoError(t, mgr.DeleteTaskRuntime(ctx, "crypto", "task-a"))
	pendingRetry, err := mgr.FetchRetries().Get(ctx, "crypto", "pending-period-failure")
	require.NoError(t, err, "task deletion must preserve unresolved durable Storage reporting work")
	require.Equal(t, domain.PeriodFailureReportPending, pendingRetry.PeriodFailureReportState)
	_, err = mgr.FetchRetries().Get(ctx, "crypto", "settled-period-failure")
	require.Error(t, err, "settled receipts may be cleaned up with their deleted target")
	targets, err := mgr.TaskInstances().ListWriteTargets(ctx, "crypto", "shared")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, "task-b", targets[0].TaskID)
	_, err = mgr.TaskInstances().Get(ctx, "crypto", "shared")
	require.NoError(t, err, "shared market-fetch instance must survive while task-b still references it")
	var parentCount int64
	require.NoError(t, mgr.db.Raw(`SELECT count(*) FROM t_period_readiness WHERE c_dataset_id = ?`, "bars-a").Scan(&parentCount).Error)
	require.Zero(t, parentCount)
}

func TestCreateSharedBatchSurvivesOneDisabledTarget(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", Enabled: true},
	} {
		require.NoError(t, mgr.Tasks().Create(ctx, task))
	}
	require.NoError(t, mgr.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{{SpaceID: "crypto", InstanceID: "shared", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m"}}))
	require.NoError(t, mgr.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-a", DatasetID: "bars-a", Status: "pending"},
		{ID: "target-b", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-b", DatasetID: "bars-b", Status: "pending"},
	}))

	require.NoError(t, mgr.Tasks().SetEnabled(ctx, "crypto", "task-a", false))
	created, err := mgr.FetchBatches().CreatePlannedWithItemsForEnabledTargets(ctx, &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "batch-one-target", ScheduleID: "schedule-one-target", BatchKind: domain.BatchKindRealtime,
		Frequency: "1m", Status: domain.BatchStatusPlanned,
	}, []string{"shared"})
	require.NoError(t, err)
	require.True(t, created, "task-b is still enabled, so the shared fetch must survive task-a disable")

	require.NoError(t, mgr.Tasks().SetEnabled(ctx, "crypto", "task-b", false))
	created, err = mgr.FetchBatches().CreatePlannedWithItemsForEnabledTargets(ctx, &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "batch-no-target", ScheduleID: "schedule-no-target", BatchKind: domain.BatchKindRealtime,
		Frequency: "1m", Status: domain.BatchStatusPlanned,
	}, []string{"shared"})
	require.NoError(t, err)
	require.False(t, created, "a shared instance with no enabled WriteTarget must not dispatch")
}

func TestWaitTaskDrainUsesSharedWriteTargetIdentity(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	for _, task := range []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "task-a", TaskName: "A", DataType: "kline", Enabled: true},
		{SpaceID: "crypto", TaskID: "task-b", TaskName: "B", DataType: "kline", Enabled: true},
	} {
		require.NoError(t, mgr.Tasks().Create(ctx, task))
	}
	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "shared", DataType: "kline", SubjectID: "BTC-USDT", Frequency: "1m"}
	require.NoError(t, mgr.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, mgr.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-a", DatasetID: "bars-a", Status: "pending"},
		{ID: "target-b", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-b", DatasetID: "bars-b", Status: "pending"},
	}))
	created, err := mgr.FetchBatches().CreatePlannedWithItemsForEnabledTargets(ctx, &domain.BatchInvocation{
		SpaceID: "crypto", BatchID: "batch-shared", ScheduleID: "schedule-shared", BatchKind: domain.BatchKindRealtime,
		Frequency: "1m", Status: domain.BatchStatusPlanned,
	}, []string{"shared"})
	require.NoError(t, err)
	require.True(t, created)

	err = mgr.WaitTaskDrain(ctx, "crypto", "task-b", 20*time.Millisecond)
	require.ErrorContains(t, err, "active fetch batch")
	_, err = mgr.FetchBatches().Complete(ctx, &domain.BatchInvocation{SpaceID: "crypto", BatchID: "batch-shared", Status: domain.BatchStatusSucceeded})
	require.NoError(t, err)
	require.NoError(t, mgr.WaitTaskDrain(ctx, "crypto", "task-b", 20*time.Millisecond))
}

func TestSchemaRetiresRedundantRetryIndexesWithoutRemovingWork(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	require.NoError(t, s.FetchRetries().Upsert(ctx, &domain.RetryItem{SpaceID: "crypto", RetryKey: "retained-work", Status: "pending", TargetDataTime: time.Now().UTC()}))
	require.NoError(t, s.db.Exec(`
CREATE INDEX idx_collector_fetch_retry_due ON t_collector_fetch_retry_items (c_status, c_next_retry_at);
CREATE INDEX idx_collector_fetch_retry_terminal_cleanup ON t_collector_fetch_retry_items (c_mtime, c_status, c_period_failure_report_state, c_id);
CREATE INDEX idx_collector_fetch_retry_period_failure ON t_collector_fetch_retry_items (c_space_id, c_status, c_period_failure_report_state, c_retry_key);
`).Error)
	for range 2 {
		require.NoError(t, s.ApplySchema(schema.AllSQL()))
	}
	stored, err := s.FetchRetries().Get(ctx, "crypto", "retained-work")
	require.NoError(t, err)
	require.Equal(t, "pending", stored.Status)
	var obsolete int64
	require.NoError(t, s.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN (?, ?, ?)`, "idx_collector_fetch_retry_due", "idx_collector_fetch_retry_terminal_cleanup", "idx_collector_fetch_retry_period_failure").Scan(&obsolete).Error)
	require.Zero(t, obsolete)
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	require.NoError(t, s.db.Raw(`EXPLAIN QUERY PLAN SELECT c_retry_key FROM t_collector_fetch_retry_items WHERE c_space_id = ? AND c_status = ? AND c_period_failure_report_state = ? AND c_retry_key > ? ORDER BY c_retry_key LIMIT ?`, "crypto", "permanent_failed", "pending", "", 100).Scan(&plan).Error)
	require.Contains(t, fmt.Sprint(plan), "idx_collector_fetch_retry_failure_reports")
	require.NotContains(t, fmt.Sprint(plan), "TEMP B-TREE")
}

func TestSQLiteTimestampsSupportDateFunctionsAndDeadlineQueries(t *testing.T) {
	s := newCollectorStore(t)
	ctx := context.Background()
	deadline := time.Date(2026, time.October, 9, 13, 14, 15, 123456789, time.UTC)
	created, err := s.FetchBatches().CreatePlanned(ctx, &domain.BatchInvocation{SpaceID: "crypto", BatchID: "sqlite-time", ScheduleID: "sqlite-time", Status: domain.BatchStatusPlanned, DeadlineAt: &deadline})
	require.NoError(t, err)
	require.True(t, created)
	var stored struct {
		Text   string   `gorm:"column:encoded"`
		Julian *float64 `gorm:"column:julian"`
	}
	require.NoError(t, s.db.Raw(`SELECT CAST(c_deadline_at AS TEXT) AS encoded, julianday(c_deadline_at) AS julian FROM t_collector_fetch_batches WHERE c_batch_id = ?`, "sqlite-time").Scan(&stored).Error)
	require.Equal(t, deadline.Format("2006-01-02 15:04:05.999999999-07:00"), stored.Text)
	require.NotNil(t, stored.Julian, "SQLite must parse the persisted timestamp")
	due, err := s.FetchBatches().ListDue(ctx, "crypto", deadline, 10)
	require.NoError(t, err)
	require.Len(t, due, 1, "an exact persisted deadline must compare equal to a bound time")
}
