package store

import (
	"context"
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
	require.NotNil(t, mgr.Tasks())
	var absent *Store
	require.Nil(t, absent.Tasks())
	var count int64
	if err := mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 't_collector_%'`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Open() created %d collector tables, want 0", count)
	}
}

func TestApplySchemaCreatesCurrentTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() must be idempotent: %v", err)
	}
	count := func(query string, args ...any) int64 {
		var n int64
		if err := mgr.db.Raw(query, args...).Scan(&n).Error; err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		return n
	}
	for _, table := range []string{"t_collector_tasks", "t_collector_task_tags", "t_collector_resample_snapshots", "t_collector_resample_jobs", "t_collector_backfill_notices", "t_collector_event_outbox"} {
		require.EqualValues(t, 1, count("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table), table)
	}
	for _, table := range []string{"t_collector_task_instances", "t_collector_instance_write_targets", "t_period_readiness", "t_period_readiness_items", "t_collector_run_tasks", "t_collector_run_series", "t_collector_task_series"} {
		require.Zero(t, count("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table), "removed table %s must not exist", table)
	}
	for table, columns := range map[string][]string{
		"t_collector_tasks":     {"c_definition_hash", "c_result_dataset_id"},
		"t_collector_task_tags": {"c_task_id", "c_tag_id"},
	} {
		for _, column := range columns {
			require.EqualValues(t, 1, count("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column), "%s.%s", table, column)
		}
	}
	// 废弃的 syncing 状态已被移除，CHECK 约束拒绝写入。
	err = mgr.db.Exec(`INSERT INTO t_collector_resample_jobs (c_job_id, c_space_id, c_task_id, c_request_id, c_start, c_end, c_next_bucket, c_status) VALUES ('bad', 'crypto', 't', 'r', 1, 2, 1, 'syncing')`).Error
	require.Error(t, err, "syncing 状态必须被 CHECK 约束拒绝")
}

func TestDeleteTaskRuntimeCleansAllOwnedStateAndPreservesSpaceWideJobs(t *testing.T) {
	ctx := context.Background()
	mgr := openResampleStore(t)
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	require.NoError(t, mgr.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-del", TaskName: "del", DataType: domain.DataTypeKline, Ingest: domain.IngestStreaming,
		ResultDatasetID: "dataset_del", Enabled: true,
	}))
	require.NoError(t, mgr.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-keep", TaskName: "keep", DataType: domain.DataTypeKline, Ingest: domain.IngestStreaming,
		ResultDatasetID: "dataset_keep", Enabled: true,
	}))
	require.NoError(t, mgr.Resample().AddSnapshot(ctx, ResampleSnapshot{SpaceID: "crypto", TaskID: "task-del", EffectiveFrom: 100, SeriesHash: "h1", Subjects: []string{"BTC"}}))
	_, _, err := mgr.Resample().SubmitJob(ctx, ResampleJob{JobID: "rj-del", SpaceID: "crypto", TaskID: "task-del", RequestID: "req-1", Start: from, End: to})
	require.NoError(t, err)
	_, err = mgr.Backfill().CreateHistorySeed(ctx, HistorySeed{
		SpaceID: "crypto", DatasetID: "dataset_del", Frequency: "1m", Status: SeedRunning, TargetFrom: from, EffectiveFrom: to, StartedAt: from,
	})
	require.NoError(t, err)
	require.NoError(t, mgr.Backfill().SetWatermark(ctx, "crypto", "dataset_del", "1m", to))
	require.NoError(t, mgr.Backfill().AddAbsences(ctx, []SourceAbsence{{SpaceID: "crypto", DatasetID: "dataset_del", Frequency: "1m", SubjectID: "BTC", From: from, To: to, Bars: 60}}))
	require.NoError(t, mgr.Resample().AddNotices(ctx, []BackfillNotice{{SpaceID: "crypto", DatasetID: "dataset_del", Frequency: "1m", SubjectID: "BTC", Start: from, End: to, Origin: BackfillSourceAuto}}))
	require.NoError(t, mgr.Backfill().CreateJob(ctx, BackfillJob{JobID: "bf-del", SpaceID: "crypto", DatasetID: "dataset_del", Frequency: "1m", From: from, To: to}, []BackfillChunk{{DatasetID: "dataset_del", Frequency: "1m", SubjectID: "BTC", From: from, To: to, ChunkID: "c1"}}))
	// 整空间作业与另一任务的数据均不得被误删。
	require.NoError(t, mgr.Backfill().CreateJob(ctx, BackfillJob{JobID: "bf-space", SpaceID: "crypto", From: from, To: to}, []BackfillChunk{{DatasetID: "dataset_keep", Frequency: "1m", SubjectID: "ETH", From: from, To: to, ChunkID: "c2"}}))
	require.NoError(t, mgr.Backfill().SetWatermark(ctx, "crypto", "dataset_keep", "1m", to))

	require.NoError(t, mgr.DeleteTaskRuntime(ctx, "crypto", "task-del"))

	_, ok, err := mgr.Resample().LatestSnapshot(ctx, "crypto", "task-del")
	require.NoError(t, err)
	require.False(t, ok)
	jobs, err := mgr.Resample().ListJobs(ctx, "crypto", "task-del", "", 10)
	require.NoError(t, err)
	require.Empty(t, jobs)
	_, ok, err = mgr.Backfill().HistorySeedOf(ctx, "crypto", "dataset_del", "1m")
	require.NoError(t, err)
	require.False(t, ok)
	wm, err := mgr.Backfill().Watermark(ctx, "crypto", "dataset_del", "1m")
	require.NoError(t, err)
	require.True(t, wm.IsZero())
	absences, err := mgr.Backfill().AbsencesIn(ctx, "crypto", "dataset_del", "1m", nil, from, to)
	require.NoError(t, err)
	require.Empty(t, absences)
	notices, err := mgr.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, notices)
	deletedJob, chunks, err := mgr.Backfill().GetJob(ctx, "bf-del")
	require.NoError(t, err)
	require.Nil(t, deletedJob)
	require.Empty(t, chunks)

	keptJob, keptChunks, err := mgr.Backfill().GetJob(ctx, "bf-space")
	require.NoError(t, err)
	require.NotNil(t, keptJob)
	require.Len(t, keptChunks, 1)
	keptWM, err := mgr.Backfill().Watermark(ctx, "crypto", "dataset_keep", "1m")
	require.NoError(t, err)
	require.True(t, keptWM.Equal(to))
}
