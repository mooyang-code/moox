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
	history, err := repo.AppliedSince(ctx, "crypto", time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, history, 1)
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
	require.NoError(t, repo.CreateJob(ctx, BackfillJob{JobID: "job-1", SpaceID: "crypto", Request: `{}`}, chunks))
	job, stored, err := repo.GetJob(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, BackfillPending, job.Status)
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

	require.NoError(t, repo.FinishChunk(ctx, claimed[0].ID, BackfillSucceeded, 100, ""))
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
