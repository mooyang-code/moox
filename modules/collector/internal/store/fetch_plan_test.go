package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func openPlanTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	require.NoError(t, s.ApplySchema(schema.AllSQL()))
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestFetchPlanAllowsOneOpenPlanPerSpace(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).FetchPlans()
	record, err := repo.Create(ctx, "crypto", `{"v":1}`, "")
	require.NoError(t, err)
	require.Equal(t, FetchPlanPending, record.Status)

	_, err = repo.Create(ctx, "crypto", `{"v":2}`, "")
	require.ErrorIs(t, err, ErrFetchPlanOpen, "同一空间已有未生效的计划时不能再保存新计划")
	other, err := repo.Create(ctx, "stockcn", `{"v":1}`, "")
	require.NoError(t, err)
	require.NotZero(t, other.ID)

	open, err := repo.Open(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, record.ID, open.ID)
	require.NoError(t, repo.Update(ctx, record.ID, FetchPlanApplying, `{"registered":true}`, ""))
	open, _ = repo.Open(ctx, "crypto")
	require.Equal(t, FetchPlanApplying, open.Status)
	require.Equal(t, `{"registered":true}`, open.Progress)

	applied, err := repo.LatestApplied(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, applied)
	require.NoError(t, repo.Update(ctx, record.ID, FetchPlanApplied, `{}`, ""))
	open, _ = repo.Open(ctx, "crypto")
	require.Nil(t, open)
	applied, _ = repo.LatestApplied(ctx, "crypto")
	require.Equal(t, record.ID, applied.ID)

	next, err := repo.Create(ctx, "crypto", `{"v":3}`, "")
	require.NoError(t, err, "已生效之后可以保存下一份计划")
	require.Greater(t, next.ID, record.ID)
	history, err := repo.ListAll(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, history, 2, "ListAll 返回全部计划（含未生效的），按先后顺序")
	require.Error(t, repo.Update(ctx, history[0].ID, FetchPlanPending, `{}`, ""), "已生效的计划不能被改回其他状态")
	again, _ := repo.LatestApplied(ctx, "crypto")
	require.Equal(t, FetchPlanApplied, again.Status)
}

func TestBackfillJobChunksClaimAndWatermark(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).Backfill()
	from := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	chunks := []BackfillChunk{
		{DatasetID: "d", Frequency: "1m", SubjectID: "BTC-USDT", From: from, To: from.Add(time.Hour), ChunkID: "c1"},
		{DatasetID: "d", Frequency: "1m", SubjectID: "ETH-USDT", From: from, To: from.Add(time.Hour), ChunkID: "c2"},
		{DatasetID: "d", Frequency: "1m", SubjectID: "SOL-USDT", From: from, To: from.Add(time.Hour), ChunkID: "c3"},
	}
	require.NoError(t, repo.CreateJob(ctx, BackfillJob{JobID: "job-1", SpaceID: "crypto", DatasetID: "d", Frequency: "1m", From: from, To: from.Add(time.Hour)}, chunks))
	job, stored, err := repo.GetJob(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, BackfillPending, job.Status)
	require.Equal(t, BackfillKindGap, job.Kind)
	require.Equal(t, BackfillSourceManual, job.Source)
	require.True(t, job.From.Equal(from) && job.To.Equal(from.Add(time.Hour)))
	require.Len(t, stored, 3)

	claimed, err := repo.ClaimChunks(ctx, "job-1", 2, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	inflight, err := repo.InFlightChunks(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, 2, inflight, "结果不明的在途分块保留名额直到最迟结束时间")
	again, err := repo.ClaimChunks(ctx, "job-1", 5, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, again, 1, "运行中且未超时的分块不会被重复取出")

	require.NoError(t, repo.FinishChunk(ctx, claimed[0].ID, claimed[0].Attempts, BackfillSucceeded, 100, ""))
	counts, err := repo.ChunkStatusCounts(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, 1, counts[BackfillSucceeded])
	require.NoError(t, repo.CancelJob(ctx, "job-1"))
	job, _, _ = repo.GetJob(ctx, "job-1")
	require.Equal(t, BackfillCanceled, job.Status)

	require.NoError(t, repo.SetWatermark(ctx, "crypto", "d", "1m", from.Add(time.Hour)))
	require.NoError(t, repo.SetWatermark(ctx, "crypto", "d", "1m", from), "水位只前进不后退")
	mark, err := repo.Watermark(ctx, "crypto", "d", "1m")
	require.NoError(t, err)
	require.True(t, mark.Equal(from.Add(time.Hour)))
}

func TestBackfillRequeueRetryAndListFilters(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).Backfill()
	from := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	chunk := func(subject, id string) BackfillChunk {
		return BackfillChunk{DatasetID: "d", Frequency: "1m", SubjectID: subject, From: from, To: from.Add(time.Hour), ChunkID: id}
	}
	require.NoError(t, repo.CreateJob(ctx, BackfillJob{JobID: "manual", SpaceID: "crypto", From: from, To: from.Add(time.Hour)}, []BackfillChunk{chunk("A", "m1"), chunk("B", "m2")}))
	require.NoError(t, repo.CreateJob(ctx, BackfillJob{JobID: "auto", SpaceID: "crypto", Source: BackfillSourceAuto, DatasetID: "d", Frequency: "1m", From: from, To: from.Add(time.Hour), AdvanceWatermark: true}, []BackfillChunk{chunk("A", "a1")}))

	claimed, err := repo.ClaimChunks(ctx, "manual", 2, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Len(t, claimed, 2)
	require.NoError(t, repo.RequeueChunk(ctx, claimed[0].ID, claimed[0].Attempts, "1@2", "2 bars still missing"))
	require.ErrorIs(t, repo.RequeueChunk(ctx, claimed[0].ID, claimed[0].Attempts, "1@2", ""), ErrChunkLost, "退回后旧尝试不再持有分块")
	_, chunks, err := repo.GetJob(ctx, "manual")
	require.NoError(t, err)
	require.Equal(t, BackfillPending, chunks[0].Status)
	require.Equal(t, "1@2", chunks[0].Missing)
	require.Equal(t, 1, chunks[0].Attempts, "退回不退尝试次数")

	_, err = repo.RetryJob(ctx, "manual")
	require.ErrorIs(t, err, ErrJobNotRetryable, "未结束的作业不能重试")
	require.NoError(t, repo.FinishChunk(ctx, claimed[1].ID, claimed[1].Attempts, BackfillFailed, 0, "boom"))
	require.NoError(t, repo.SetJobStatus(ctx, "manual", BackfillFailed, "1 chunks failed"))
	retried, err := repo.RetryJob(ctx, "manual")
	require.NoError(t, err)
	require.Equal(t, 1, retried)
	job, chunks, err := repo.GetJob(ctx, "manual")
	require.NoError(t, err)
	require.Equal(t, BackfillPending, job.Status)
	require.Empty(t, job.LastError)
	require.Equal(t, BackfillPending, chunks[1].Status)
	require.Zero(t, chunks[1].Attempts)

	all, total, err := repo.ListJobs(ctx, "crypto", BackfillJobFilter{}, 1, 10)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Equal(t, "auto", all[0].JobID, "新作业在前")
	autos, _, err := repo.ListJobs(ctx, "crypto", BackfillJobFilter{Source: BackfillSourceAuto}, 1, 10)
	require.NoError(t, err)
	require.Len(t, autos, 1)
	require.True(t, autos[0].AdvanceWatermark)
	byDataset, _, err := repo.ListJobs(ctx, "crypto", BackfillJobFilter{DatasetID: "d"}, 1, 10)
	require.NoError(t, err)
	require.Len(t, byDataset, 2, "不限数据集的手动作业也算覆盖该数据集")
	require.NoError(t, repo.SetJobStatus(ctx, "auto", BackfillSucceeded, ""))
	attention, _, err := repo.ListJobs(ctx, "crypto", BackfillJobFilter{Attention: true}, 1, 10)
	require.NoError(t, err)
	require.Len(t, attention, 1)
	require.Equal(t, "manual", attention[0].JobID)

	counts, err := repo.ChunkStatusCountsOf(ctx, []string{"manual", "auto"})
	require.NoError(t, err)
	require.Equal(t, 2, counts["manual"][BackfillPending])
	require.Equal(t, 1, counts["auto"][BackfillPending])
	autoChunks, err := repo.AutoChunksSince(ctx, "crypto", from, 100)
	require.NoError(t, err)
	require.Len(t, autoChunks, 1)
	jobCounts, err := repo.JobCounts(ctx, "crypto", from)
	require.NoError(t, err)
	require.Equal(t, BackfillJobCounts{Active: 1}, jobCounts)
}

func TestBackfillFailedChunkCountsAndCleanup(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).Backfill()
	from := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	chunk := BackfillChunk{DatasetID: "d", Frequency: "1m", SubjectID: "A", From: from, To: from.Add(time.Hour), ChunkID: "c"}
	require.NoError(t, repo.CreateJob(ctx, BackfillJob{JobID: "old-auto", SpaceID: "crypto", Source: BackfillSourceAuto, From: from, To: from.Add(time.Hour)}, []BackfillChunk{chunk}))
	require.NoError(t, repo.CreateJob(ctx, BackfillJob{JobID: "old-manual", SpaceID: "crypto", From: from, To: from.Add(time.Hour)}, []BackfillChunk{chunk}))
	claimed, err := repo.ClaimChunks(ctx, "old-auto", 1, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, repo.FinishChunk(ctx, claimed[0].ID, claimed[0].Attempts, BackfillFailed, 0, "boom"))
	failed, err := repo.FailedChunkCounts(ctx, "crypto", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, map[string]int{"d|1m": 1}, failed)

	require.NoError(t, repo.SetJobStatus(ctx, "old-auto", BackfillFailed, ""))
	require.NoError(t, repo.SetJobStatus(ctx, "old-manual", BackfillSucceeded, ""))
	future := time.Now().Add(time.Hour)
	deleted, err := repo.CleanupFinishedJobs(ctx, "crypto", future, time.Now().Add(-time.Hour), 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted, "只有超过保留期的自动作业被删除")
	job, _, err := repo.GetJob(ctx, "old-auto")
	require.NoError(t, err)
	require.Nil(t, job)
	job, _, err = repo.GetJob(ctx, "old-manual")
	require.NoError(t, err)
	require.NotNil(t, job)
}

func TestSourceAbsencesAndHistoryScans(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).Backfill()
	from := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.AddAbsences(ctx, []SourceAbsence{
		{SpaceID: "crypto", DatasetID: "d", Frequency: "1m", SubjectID: "A", From: from, To: from.Add(2 * time.Minute), Bars: 2},
		{SpaceID: "crypto", DatasetID: "d", Frequency: "1m", SubjectID: "B", From: from.Add(time.Hour), To: from.Add(time.Hour + time.Minute), Bars: 1},
	}))
	require.NoError(t, repo.AddAbsences(ctx, []SourceAbsence{{SpaceID: "crypto", DatasetID: "d", Frequency: "1m", SubjectID: "A", From: from, To: from.Add(3 * time.Minute), Bars: 3}}), "同一起点重复确认取更长的区间")
	found, err := repo.AbsencesIn(ctx, "crypto", "d", "1m", []string{"A"}, from.Add(time.Minute), from.Add(10*time.Minute))
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.True(t, found[0].To.Equal(from.Add(3*time.Minute)))
	none, err := repo.AbsencesIn(ctx, "crypto", "d", "1m", nil, from.Add(3*time.Minute), from.Add(10*time.Minute))
	require.NoError(t, err)
	require.Empty(t, none, "区间右开，不相交")
	bars, err := repo.AbsentBars(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"d|1m": 4}, bars)

	require.NoError(t, repo.SaveHistoryScan(ctx, HistoryScan{SpaceID: "crypto", DatasetID: "d", Frequency: "1m", ScannedAt: from, From: from.Add(-time.Hour), To: from, GapBars: 5, GapSubjects: 2, Gaps: `[{"from":"x"}]`}))
	scans, err := repo.HistoryScans(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, 5, scans["d|1m"].GapBars)
	require.True(t, scans["d|1m"].To.Equal(from))

	cleared, err := repo.ClearAbsences(ctx, "crypto", "d", "1m")
	require.NoError(t, err)
	require.EqualValues(t, 2, cleared)
	scans, err = repo.HistoryScans(ctx, "crypto")
	require.NoError(t, err)
	require.Empty(t, scans, "清除缺失后重新扫描历史")
}

func TestFetchPlanSpacesListsEverySpaceThatEverHadAPlan(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).FetchPlans()
	spaces, err := repo.Spaces(ctx)
	require.NoError(t, err)
	require.Empty(t, spaces)
	_, err = repo.Create(ctx, "stockcn", `{"v":1}`, "")
	require.NoError(t, err)
	record, err := repo.Create(ctx, "crypto", `{"v":1}`, "")
	require.NoError(t, err)
	require.NoError(t, repo.Update(ctx, record.ID, FetchPlanApplied, "{}", ""))
	spaces, err = repo.Spaces(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"crypto", "stockcn"}, spaces)
}

func TestFetchPlanPauseSwitchIsPersistedPerSpace(t *testing.T) {
	ctx := context.Background()
	repo := openPlanTestStore(t).FetchPlans()
	paused, err := repo.PausedSpaces(ctx)
	require.NoError(t, err)
	require.Empty(t, paused)

	require.NoError(t, repo.SetPaused(ctx, "Crypto", true))
	require.NoError(t, repo.SetPaused(ctx, "crypto", true), "重复保存没有副作用")
	require.NoError(t, repo.SetPaused(ctx, "stockcn", true))
	paused, err = repo.PausedSpaces(ctx)
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"crypto": true, "stockcn": true}, paused)

	require.NoError(t, repo.SetPaused(ctx, "crypto", false))
	paused, _ = repo.PausedSpaces(ctx)
	require.Equal(t, map[string]bool{"stockcn": true}, paused)
	require.Error(t, repo.SetPaused(ctx, " ", true))
}
