package store

import (
	"context"
	"path/filepath"
	"strings"
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
	for _, table := range []string{"t_collector_tasks", "t_collector_task_tags", "t_collector_task_series", "t_collector_task_period_series", "t_collector_runs", "t_collector_task_instances", "t_collector_instance_write_targets", "t_collector_fetch_batches", "t_collector_fetch_batch_items", "t_collector_fetch_retry_items", "t_period_readiness", "t_period_readiness_items"} {
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
		"t_collector_runs":                   {"c_run_id", "c_run_key", "c_run_type"},
		"t_collector_task_instances":         {"c_instance_id", "c_run_id", "c_request_key", "c_source_id", "c_series_tag"},
		"t_collector_instance_write_targets": {"c_write_target_id", "c_instance_id", "c_task_id", "c_dataset_id", "c_series_index", "c_series_hash", "c_expected_count"},
		"t_collector_fetch_batches":          {"c_instance_id", "c_write_target_id", "c_retry_scope"},
		"t_collector_fetch_batch_items":      {"c_batch_id", "c_instance_id"},
		"t_collector_fetch_retry_items":      {"c_instance_id", "c_write_target_id", "c_retry_scope", "c_failure_targets_json", "c_period_failure_reported"},
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
	for _, index := range []string{"idx_collector_instances_run", "idx_collector_batch_items_instance_batch", "idx_collector_task_period_series_lookup", "idx_collector_task_period_series_key", "idx_collector_task_period_series_retention", "idx_collector_fetch_retry_period_failure"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&count).Error; err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("index %s count = %d, want 1", index, count)
		}
	}
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

func TestApplySchemaRejectsLegacyRuleTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.db.Exec(`CREATE TABLE t_collector_task_rules (c_id INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err == nil {
		t.Fatal("ApplySchema() succeeded with a legacy Collector table")
	} else if !strings.Contains(err.Error(), "t_collector_task_rules") {
		t.Fatalf("ApplySchema() error = %v, want legacy table diagnostic", err)
	}
}

func TestApplySchemaRejectsIncompleteCurrentTaskSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.db.Exec(`CREATE TABLE t_collector_tasks (c_id INTEGER PRIMARY KEY, c_task_id TEXT NOT NULL)`).Error)
	err = mgr.ApplySchema(schema.AllSQL())
	require.Error(t, err)
	require.ErrorContains(t, err, "collector schema reset required")
	require.ErrorContains(t, err, "current task schema is incomplete")
}

func TestApplySchemaRejectsIncompletePeriodSeriesSchema(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.db.Exec(`CREATE TABLE t_collector_task_period_series (c_id INTEGER PRIMARY KEY, c_space_id TEXT NOT NULL)`).Error)

	err = mgr.ApplySchema(schema.AllSQL())
	require.Error(t, err)
	require.ErrorContains(t, err, "collector schema reset required")
	require.ErrorContains(t, err, "t_collector_task_period_series.c_dataset_id missing")
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
	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "shared", CollectionTaskID: "task-a", DataType: "kline", DatasetID: "bars-a", SubjectID: "BTC-USDT", Frequency: "1m"}
	require.NoError(t, mgr.TaskInstances().UpsertMany(ctx, []domain.TaskInstance{instance}))
	require.NoError(t, mgr.TaskInstances().UpsertWriteTargets(ctx, []domain.WriteTarget{
		{ID: "target-a", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-a", DatasetID: "bars-a", Status: "pending"},
		{ID: "target-b", SpaceID: "crypto", InstanceID: "shared", TaskID: "task-b", DatasetID: "bars-b", Status: "pending"},
	}))
	period := time.Now().UTC().Truncate(time.Minute)
	_, err = mgr.PeriodReadiness().EnsurePeriod(ctx, domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars-a", Frequency: "1m", PeriodTime: period},
		DeadlineAt: period.Add(time.Minute),
		Tasks:      []domain.PeriodTaskSeed{{InstanceID: "shared", WriteTargetID: "target-a", SubjectID: "BTC-USDT"}},
	})
	require.NoError(t, err)

	require.NoError(t, mgr.DeleteTaskRuntime(ctx, "crypto", "task-a"))
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
	instance := domain.TaskInstance{SpaceID: "crypto", InstanceID: "shared", CollectionTaskID: "task-a", DataType: "kline", DatasetID: "bars-a", SubjectID: "BTC-USDT", Frequency: "1m"}
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
