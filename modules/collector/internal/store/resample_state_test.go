package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func openResampleStore(t *testing.T) *Store {
	t.Helper()
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	return mgr
}

func resampleJob(id, request string, start, end time.Time) ResampleJob {
	return ResampleJob{JobID: id, SpaceID: "crypto", TaskID: "task", RequestID: request, Origin: ResampleOriginManual, Start: start, End: end, NotBefore: start}
}

func TestResampleSnapshotsResolveByEffectiveFrom(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	require.NoError(t, repo.AddSnapshot(ctx, ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: 1000, SeriesHash: "h1", Subjects: []string{"A", "B"}}))
	require.NoError(t, repo.AddSnapshot(ctx, ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: 2000, SeriesHash: "h2", Subjects: []string{"A", "B", "C"}}))
	require.NoError(t, repo.AddSnapshot(ctx, ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: 2000, SeriesHash: "h2", Subjects: []string{"A", "B", "C"}}), "同一生效点重复登记是幂等的")

	_, ok, err := repo.SnapshotFor(ctx, "crypto", "task", 999)
	require.NoError(t, err)
	require.False(t, ok, "生效点之前没有快照")
	snapshot, ok, err := repo.SnapshotFor(ctx, "crypto", "task", 1999)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "h1", snapshot.SeriesHash)
	snapshot, _, _ = repo.SnapshotFor(ctx, "crypto", "task", 2000)
	require.Equal(t, "h2", snapshot.SeriesHash)
	require.Equal(t, []string{"A", "B", "C"}, snapshot.Subjects)
	latest, ok, err := repo.LatestSnapshot(ctx, "crypto", "task")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(2000), latest.EffectiveFrom)
}

func TestSubmitJobIsIdempotentByRequestID(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	first, created, err := repo.SubmitJob(ctx, resampleJob("job-1", "req-1", start, start.Add(24*time.Hour)))
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, ResampleJobQueued, first.Status)
	require.Equal(t, start, first.NextBucket)

	again, created, err := repo.SubmitJob(ctx, resampleJob("job-2", "req-1", start, start.Add(48*time.Hour)))
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, "job-1", again.JobID, "同一个 request_id 返回已有作业")
}

func TestSourceBackfillJobsMergeWhenOverlappingAndNotStarted(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	submit := func(id, request string, start, end time.Time, subjects []string, notBefore time.Time) (ResampleJob, bool) {
		job := ResampleJob{JobID: id, SpaceID: "crypto", TaskID: "task", RequestID: request, Origin: ResampleOriginSourceBackfill, Subjects: subjects, Start: start, End: end, NotBefore: notBefore}
		got, created, err := repo.SubmitJob(ctx, job)
		require.NoError(t, err)
		return got, created
	}
	first, created := submit("a", "r1", base, base.Add(time.Hour), []string{"A"}, base)
	require.True(t, created)
	merged, created := submit("b", "r2", base.Add(time.Hour), base.Add(2*time.Hour), []string{"B"}, base.Add(time.Minute))
	require.False(t, created, "相邻范围合并进已有排队作业")
	require.Equal(t, first.JobID, merged.JobID)
	require.Equal(t, base, merged.Start)
	require.Equal(t, base.Add(2*time.Hour), merged.End)
	require.Equal(t, []string{"A", "B"}, merged.Subjects)
	require.Equal(t, base.Add(time.Minute), merged.NotBefore, "最早开始时间取较晚的一个")

	allSubjects, created := submit("c", "r3", base, base.Add(30*time.Minute), nil, base)
	require.False(t, created)
	require.Empty(t, allSubjects.Subjects, "有一个是全部标的，并集仍是全部")

	_, created = submit("d", "r4", base.Add(10*time.Hour), base.Add(11*time.Hour), []string{"A"}, base)
	require.True(t, created, "不相邻的范围单独成作业")

	manual, created, err := repo.SubmitJob(ctx, resampleJob("e", "r5", base, base.Add(time.Hour)))
	require.NoError(t, err)
	require.True(t, created, "手动作业不合并")
	require.Equal(t, ResampleOriginManual, manual.Origin)
}

func TestRunnableJobsOneRunningJobPerTaskRespectingNotBefore(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	other := resampleJob("other", "r", now.Add(-time.Hour), now)
	other.TaskID = "other-task"
	_, _, err := repo.SubmitJob(ctx, resampleJob("first", "r1", now.Add(-2*time.Hour), now.Add(-time.Hour)))
	require.NoError(t, err)
	_, _, err = repo.SubmitJob(ctx, resampleJob("second", "r2", now.Add(-2*time.Hour), now.Add(-time.Hour)))
	require.NoError(t, err)
	_, _, err = repo.SubmitJob(ctx, other)
	require.NoError(t, err)
	delayed := resampleJob("delayed", "r3", now.Add(-time.Hour), now)
	delayed.TaskID = "delayed-task"
	delayed.NotBefore = now.Add(time.Minute)
	_, _, err = repo.SubmitJob(ctx, delayed)
	require.NoError(t, err)

	runnable, err := repo.RunnableJobs(ctx, now, 10)
	require.NoError(t, err)
	var ids []string
	for _, job := range runnable {
		ids = append(ids, job.JobID)
	}
	require.Equal(t, []string{"first", "other"}, ids, "同一任务只取最早的一个，没到开始时间的不取")
}

func TestJobProgressStatusAndCancel(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	_, _, err := repo.SubmitJob(ctx, resampleJob("job", "r", start, start.Add(3*time.Hour)))
	require.NoError(t, err)
	started, ok, err := repo.StartJob(ctx, "job", start)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ResampleJobRunning, started.Status)
	require.NoError(t, repo.AdvanceJob(ctx, "job", JobProgress{NextBucket: start.Add(time.Hour), DoneBuckets: 1, Written: 10, Skipped: 2, SkippedSamples: []string{"S1@t0"}}))
	require.NoError(t, repo.AdvanceJob(ctx, "job", JobProgress{NextBucket: start.Add(2 * time.Hour), DoneBuckets: 1, Written: 5, Skipped: 1, SkippedSamples: []string{"S2@t1"}}))
	job, err := repo.GetJob(ctx, "crypto", "job")
	require.NoError(t, err)
	require.Equal(t, ResampleJobRunning, job.Status)
	require.Equal(t, 2, job.DoneBuckets)
	require.EqualValues(t, 15, job.Written)
	require.EqualValues(t, 3, job.Skipped)
	require.Equal(t, []string{"S1@t0", "S2@t1"}, job.SkippedSamples)
	require.Equal(t, start.Add(2*time.Hour), job.NextBucket)

	cancelled, err := repo.CancelJob(ctx, "crypto", "job")
	require.NoError(t, err)
	require.Equal(t, ResampleJobCancelled, cancelled.Status)
	require.NoError(t, repo.AdvanceJob(ctx, "job", JobProgress{NextBucket: start.Add(3 * time.Hour), DoneBuckets: 1}), "取消后的推进被忽略")
	job, _ = repo.GetJob(ctx, "crypto", "job")
	require.Equal(t, 2, job.DoneBuckets)
	_, err = repo.CancelJob(ctx, "crypto", "job")
	require.ErrorIs(t, err, ErrResampleJobFinished)
	_, err = repo.GetJob(ctx, "crypto", "missing")
	require.ErrorIs(t, err, ErrResampleJobNotFound)
}

func TestNoticesSealIntoOutboxAtomically(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.AddNotices(ctx, []BackfillNotice{
		{SpaceID: "crypto", DatasetID: "d", Frequency: "5m", SubjectID: "A", Start: start, End: start.Add(time.Hour), Origin: "auto", JobID: "j"},
		{SpaceID: "crypto", DatasetID: "d", Frequency: "5m", SubjectID: "B", Start: start, End: start.Add(time.Hour), Origin: "auto", JobID: "j"},
	}))
	notices, err := repo.UnsealedNotices(ctx, 100)
	require.NoError(t, err)
	require.Len(t, notices, 2)
	ids := []int64{notices[0].ID, notices[1].ID}

	event := OutboxEvent{EventID: "batch-1", SpaceID: "crypto", EventName: "event.collector.kline.backfilled", SubjectToken: "d", Payload: []byte("payload")}
	require.NoError(t, repo.SealBatch(ctx, ids, event))
	require.NoError(t, repo.SealBatch(ctx, ids, event), "重复封存不重复写发件箱")
	left, _ := repo.UnsealedNotices(ctx, 100)
	require.Empty(t, left)
	pending, err := repo.PendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, []byte("payload"), pending[0].Payload)
	require.NoError(t, repo.MarkPublished(ctx, pending[0].ID))
	pending, _ = repo.PendingEvents(ctx, 10)
	require.Empty(t, pending)
	removed, err := repo.CleanupNotices(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 3, removed)
}

func TestFinishChunkRecordsNoticeInSameTransaction(t *testing.T) {
	mgr := openResampleStore(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_kline_backfill_jobs (c_job_id, c_space_id, c_source, c_kind, c_from, c_to, c_status) VALUES ('job', 'crypto', 'manual', 'force', ?, ?, 'running')`, start, start.Add(time.Hour)).Error)
	require.NoError(t, mgr.db.Exec(`INSERT INTO t_collector_kline_backfill_chunks (c_job_id, c_dataset_id, c_frequency, c_subject_id, c_from, c_to, c_chunk_id, c_status, c_attempts) VALUES ('job', 'dataset_src', '5m', 'BTC', ?, ?, 'chunk', 'running', 1)`, start, start.Add(time.Hour)).Error)
	var id int64
	require.NoError(t, mgr.db.Raw(`SELECT c_id FROM t_collector_kline_backfill_chunks WHERE c_chunk_id = 'chunk'`).Scan(&id).Error)

	require.NoError(t, mgr.Backfill().FinishChunk(ctx, id, 1, BackfillSucceeded, 12, ""))
	notices, err := mgr.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, notices, 1)
	require.Equal(t, "dataset_src", notices[0].DatasetID)
	require.Equal(t, "BTC", notices[0].SubjectID)
	require.Equal(t, BackfillKindForce, notices[0].Origin, "force 作业的来源记为 force")
	require.Equal(t, start, notices[0].Start)

	// 失败的分块与已不属于该次尝试的分块都不产生通知。
	require.ErrorIs(t, mgr.Backfill().FinishChunk(ctx, id, 1, BackfillSucceeded, 12, ""), ErrChunkLost)
	left, _ := mgr.Resample().UnsealedNotices(ctx, 10)
	require.Len(t, left, 1)
}
