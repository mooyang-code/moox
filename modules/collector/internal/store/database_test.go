package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

const legacyFetchRetryItemsDDL = `CREATE TABLE t_collector_fetch_retry_items (
	c_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
	c_space_id TEXT NOT NULL,
	c_retry_key TEXT NOT NULL,
	c_source_batch_id TEXT NOT NULL,
	c_batch_kind TEXT NOT NULL DEFAULT 'realtime',
	c_instance_id TEXT NOT NULL DEFAULT '',
	c_write_target_id TEXT NOT NULL DEFAULT '',
	c_retry_scope TEXT NOT NULL DEFAULT '',
	c_subject_id TEXT NOT NULL,
	c_frequency TEXT NOT NULL,
	c_target_data_time DATETIME NOT NULL,
	c_task_json TEXT NOT NULL DEFAULT '{}',
	c_attempt INTEGER NOT NULL DEFAULT 1,
	c_status TEXT NOT NULL,
	c_next_retry_at DATETIME,
	c_last_error_type TEXT NOT NULL DEFAULT '',
	c_last_error_summary TEXT NOT NULL DEFAULT '',
	c_ctime DATETIME DEFAULT CURRENT_TIMESTAMP,
	c_mtime DATETIME DEFAULT CURRENT_TIMESTAMP
)`

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
	for _, index := range []string{"idx_collector_instances_run", "idx_collector_instances_space_page", "idx_collector_instances_list_page", "idx_collector_instances_space_status_page", "idx_collector_instances_storage_write", "idx_collector_instances_terminal_cleanup_space", "idx_collector_batch_items_instance_batch", "idx_collector_task_period_series_lookup", "idx_collector_task_period_series_key", "idx_collector_task_period_series_retention", "idx_collector_task_period_series_space_retention", "idx_collector_period_storage_terminal_cleanup", "idx_collector_period_storage_waiting_cursor", "idx_collector_runs_terminal_cleanup", "idx_collector_runs_terminal_cleanup_space", "idx_collector_write_targets_retention_space", "idx_collector_fetch_batch_instance_ref", "idx_collector_fetch_batch_target_ref", "idx_collector_fetch_batch_period_due", "idx_collector_fetch_batch_terminal_items_cleanup", "idx_collector_fetch_batch_terminal_items_cleanup_space", "idx_collector_fetch_batch_terminal_parent_cleanup", "idx_collector_fetch_batch_terminal_parent_cleanup_space", "idx_collector_fetch_retry_instance_active", "idx_collector_fetch_retry_period_due", "idx_collector_fetch_retry_period_failure", "idx_collector_fetch_retry_cleanup_succeeded_space", "idx_collector_fetch_retry_cleanup_permanent_space", "idx_collector_timer_period_batches_claim_request", "idx_collector_timer_period_batches_candidate", "idx_period_readiness_items_write_target"} {
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

func TestApplySchemaAddsImmutablePeriodPriorityColumnsToCurrentDatabase(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	oldSchema := strings.ReplaceAll(schema.AllSQL(), "    c_period_time DATETIME,\n    c_period_deadline_at DATETIME,\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "    c_items_cleaned INTEGER NOT NULL DEFAULT 0,\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_terminal_items_cleanup\nON t_collector_fetch_batches (c_status, c_completed_at, c_id)\nWHERE c_items_cleaned = 0;\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_terminal_items_cleanup_space\nON t_collector_fetch_batches (c_space_id, c_status, c_completed_at, c_id)\nWHERE c_items_cleaned = 0;\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_terminal_parent_cleanup\nON t_collector_fetch_batches (c_status, c_completed_at, c_id)\nWHERE c_items_cleaned = 1;\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_terminal_parent_cleanup_space\nON t_collector_fetch_batches (c_space_id, c_status, c_completed_at, c_id)\nWHERE c_items_cleaned = 1;\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "CREATE INDEX IF NOT EXISTS idx_collector_fetch_batch_period_due\nON t_collector_fetch_batches (c_space_id, c_status, c_period_deadline_at, c_deadline_at);\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "CREATE INDEX IF NOT EXISTS idx_collector_fetch_retry_period_due\nON t_collector_fetch_retry_items (c_space_id, c_status, c_period_deadline_at, c_next_retry_at);\n", "")
	require.NoError(t, mgr.ApplySchema(oldSchema))
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	for table, columns := range map[string][]string{
		"t_collector_fetch_batches":     {"c_period_time", "c_period_deadline_at", "c_items_cleaned"},
		"t_collector_fetch_retry_items": {"c_period_time", "c_period_deadline_at"},
	} {
		for _, column := range columns {
			var count int64
			require.NoError(t, mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count).Error)
			require.EqualValues(t, 1, count, "%s.%s should be added without discarding the existing database", table, column)
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

func TestApplySchemaMigratesLegacyFetchRetryItemsAndPreservesRows(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_tasks (c_space_id, c_task_id, c_task_name, c_definition_hash, c_series_hash)
		VALUES ('crypto', 'task-17', 'task-17', 'definition-17', 'series-set-17')`).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_task_instances (c_space_id, c_instance_id, c_subject_id, c_frequency)
		VALUES ('crypto', 'instance-17', 'BTC-USDT', '1m')`).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_instance_write_targets (
		c_space_id, c_write_target_id, c_instance_id, c_task_id, c_dataset_id, c_series_index, c_series_hash, c_expected_count
	) VALUES ('crypto', 'target-17', 'instance-17', 'task-17', 'bars', 0, 'current-hash-after-roster-change', 1)`).Error)
	require.NoError(t, mgr.db.Exec(`DROP TABLE t_collector_fetch_retry_items`).Error)
	require.NoError(t, mgr.db.Exec(legacyFetchRetryItemsDDL).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (
		c_id, c_space_id, c_retry_key, c_source_batch_id, c_batch_kind, c_instance_id,
		c_write_target_id, c_retry_scope, c_subject_id, c_frequency, c_target_data_time,
		c_task_json, c_attempt, c_status, c_last_error_type, c_last_error_summary
	) VALUES (17, 'crypto', 'retry-17', 'batch-17', 'scheduled', 'instance-17',
		'target-17', 'period', 'BTC-USDT', '1m', '2026-10-02T12:00:00Z',
		'{"task":"kept"}', 3, 'permanent_failed', 'network', 'still visible')`).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (
		c_id, c_space_id, c_retry_key, c_source_batch_id, c_batch_kind, c_instance_id,
		c_write_target_id, c_retry_scope, c_subject_id, c_frequency, c_target_data_time,
		c_task_json, c_attempt, c_status, c_last_error_type, c_last_error_summary
	) VALUES (18, 'crypto', 'retry-18', 'batch-18', 'scheduled', 'deleted-instance',
		'', 'period', 'ETH-USDT', '1m', '2026-10-02T12:00:00Z',
		'{"task":"kept"}', 2, 'permanent_failed', 'network', 'target no longer exists')`).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (
		c_id, c_space_id, c_retry_key, c_source_batch_id, c_batch_kind, c_instance_id,
		c_write_target_id, c_retry_scope, c_subject_id, c_frequency, c_target_data_time,
		c_task_json, c_attempt, c_status, c_last_error_type, c_last_error_summary
	) VALUES (19, 'crypto', 'retry-19', 'batch-19', 'scheduled', 'instance-17',
		'', 'period', 'BTC-USDT', '1m', '2026-10-02T12:00:00Z',
		'{"task":"kept"}', 2, 'pending', 'network', 'pending before upgrade')`).Error)

	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	var item domain.RetryItem
	require.NoError(t, mgr.db.Where("c_retry_key = ?", "retry-17").Take(&item).Error)
	require.Equal(t, 17, item.ID)
	require.Equal(t, "instance-17", item.InstanceID)
	require.Equal(t, "target-17", item.WriteTargetID)
	require.Equal(t, "period", item.RetryScope)
	require.Equal(t, 3, item.Attempt)
	require.Equal(t, "permanent_failed", item.Status)
	require.Equal(t, "still visible", item.LastErrorSummary)
	require.Equal(t, "[]", item.FailureTargetsJSON, "mutable current WriteTargets must not be treated as a historical period snapshot")
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, item.PeriodFailureReportState)
	var noTarget domain.RetryItem
	require.NoError(t, mgr.db.Where("c_retry_key = ?", "retry-18").Take(&noTarget).Error)
	require.Equal(t, "[]", noTarget.FailureTargetsJSON)
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, noTarget.PeriodFailureReportState)
	pending, err := mgr.FetchRetries().ListPendingPeriodFailuresAfter(context.Background(), "crypto", "", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, mgr.FetchRetries().MarkPermanent(context.Background(), "crypto", "retry-19", "retry_budget_exhausted", "upstream timed out"))
	var newlyPermanent domain.RetryItem
	require.NoError(t, mgr.db.Where("c_retry_key = ?", "retry-19").Take(&newlyPermanent).Error)
	require.Equal(t, domain.PeriodFailureReportMissedDeadline, newlyPermanent.PeriodFailureReportState)
	require.NoError(t, mgr.FetchRetries().Cleanup(context.Background(), time.Now().Add(time.Hour)))
	require.Error(t, mgr.db.Where("c_retry_key = ?", "retry-18").Take(&noTarget).Error)
	require.Error(t, mgr.db.Where("c_retry_key = ?", "retry-19").Take(&newlyPermanent).Error)
	require.Error(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (
		c_space_id, c_retry_key, c_source_batch_id, c_subject_id, c_frequency,
		c_target_data_time, c_status, c_period_failure_report_state
	) VALUES ('crypto', 'invalid-state', '', 'BTC-USDT', '1m', '2026-10-02T12:00:00Z',
		'permanent_failed', 'unknown')`).Error)
}

func TestApplySchemaRejectsBeforeMigratingLegacyFetchRetryItems(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.db.Exec(legacyFetchRetryItemsDDL).Error)
	require.NoError(t, mgr.db.Exec(`CREATE UNIQUE INDEX idx_collector_fetch_retry ON t_collector_fetch_retry_items (c_space_id, c_retry_key)`).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (c_space_id, c_retry_key, c_source_batch_id, c_subject_id, c_frequency, c_target_data_time, c_status)
		VALUES ('crypto', 'preserved', 'batch', 'BTC-USDT', '1m', '2026-10-02T12:00:00Z', 'pending')`).Error)
	require.NoError(t, mgr.db.Exec(`CREATE TABLE t_collector_task_rules (c_id INTEGER PRIMARY KEY)`).Error)

	err = mgr.ApplySchema(schema.AllSQL())
	require.ErrorContains(t, err, "t_collector_task_rules")
	var columnCount int64
	require.NoError(t, mgr.db.Raw(`SELECT count(*) FROM pragma_table_info('t_collector_fetch_retry_items')`).Scan(&columnCount).Error)
	require.EqualValues(t, 19, columnCount)
	var indexCount int64
	require.NoError(t, mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_collector_fetch_retry'`).Scan(&indexCount).Error)
	require.EqualValues(t, 1, indexCount)
	var retryKey string
	require.NoError(t, mgr.db.Raw(`SELECT c_retry_key FROM t_collector_fetch_retry_items WHERE c_id = 1`).Scan(&retryKey).Error)
	require.Equal(t, "preserved", retryKey)
}

func TestApplySchemaRollsBackLegacyFetchRetryMigrationOnSchemaError(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.db.Exec(legacyFetchRetryItemsDDL).Error)
	require.NoError(t, mgr.db.Exec(`CREATE UNIQUE INDEX idx_collector_fetch_retry ON t_collector_fetch_retry_items (c_space_id, c_retry_key)`).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_fetch_retry_items (c_space_id, c_retry_key, c_source_batch_id, c_subject_id, c_frequency, c_target_data_time, c_status)
		VALUES ('crypto', 'preserved', 'batch', 'BTC-USDT', '1m', '2026-10-02T12:00:00Z', 'pending')`).Error)

	err = mgr.ApplySchema(`CREATE TABLE t_apply_schema_partial (c_id INTEGER); THIS IS NOT SQL;`)
	require.Error(t, err)
	var columnCount int64
	require.NoError(t, mgr.db.Raw(`SELECT count(*) FROM pragma_table_info('t_collector_fetch_retry_items')`).Scan(&columnCount).Error)
	require.EqualValues(t, 19, columnCount)
	var indexCount int64
	require.NoError(t, mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_collector_fetch_retry'`).Scan(&indexCount).Error)
	require.EqualValues(t, 1, indexCount)
	var retryKey string
	require.NoError(t, mgr.db.Raw(`SELECT c_retry_key FROM t_collector_fetch_retry_items WHERE c_id = 1`).Scan(&retryKey).Error)
	require.Equal(t, "preserved", retryKey)
	var partialTableCount int64
	require.NoError(t, mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 't_apply_schema_partial'`).Scan(&partialTableCount).Error)
	require.Zero(t, partialTableCount)
}

func TestApplySchemaRejectsUnknownLegacyFetchRetryColumn(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.db.Exec(`CREATE TABLE t_collector_fetch_retry_items (
		c_id INTEGER PRIMARY KEY, c_space_id TEXT NOT NULL, c_retry_key TEXT NOT NULL,
		c_source_batch_id TEXT NOT NULL, c_batch_kind TEXT NOT NULL, c_instance_id TEXT NOT NULL,
		c_write_target_id TEXT NOT NULL, c_retry_scope TEXT NOT NULL, c_subject_id TEXT NOT NULL,
		c_frequency TEXT NOT NULL, c_target_data_time DATETIME NOT NULL, c_task_json TEXT NOT NULL,
		c_attempt INTEGER NOT NULL, c_status TEXT NOT NULL, c_next_retry_at DATETIME,
		c_last_error_type TEXT NOT NULL, c_last_error_summary TEXT NOT NULL,
		c_ctime DATETIME, c_mtime DATETIME, c_unrecognized TEXT
	)`).Error)
	err = mgr.ApplySchema(schema.AllSQL())
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported legacy Collector fetch retry schema")
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
