package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 统一数据集的快照记下每个序列下的成员；单序列快照没有 Members。
func TestResampleSnapshotKeepsMembersPerSeriesTag(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	members := map[string][]string{"spot": {"BTC", "ETH"}, "swap": {"BTC"}}
	require.NoError(t, repo.AddSnapshot(ctx, ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: 1000, SeriesHash: "h1", Subjects: []string{"BTC", "ETH"}, Members: members}))
	require.NoError(t, repo.AddSnapshot(ctx, ResampleSnapshot{SpaceID: "crypto", TaskID: "single", EffectiveFrom: 1000, SeriesHash: "h2", Subjects: []string{"BTC"}}))

	unified, ok, err := repo.LatestSnapshot(ctx, "crypto", "task")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, members, unified.Members)
	single, ok, err := repo.LatestSnapshot(ctx, "crypto", "single")
	require.NoError(t, err)
	require.True(t, ok)
	require.Empty(t, single.Members)
}

// 补采作业合并时序列取并集；任一个不限定序列，合并后仍是全部序列。
func TestSourceBackfillJobsMergeSeriesTags(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	base := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	submit := func(id, request string, tags ...string) ResampleJob {
		job := resampleJob(id, request, base, base.Add(time.Hour))
		job.Origin = ResampleOriginSourceBackfill
		job.Subjects = []string{"BTC"}
		job.SeriesTags = tags
		got, _, err := repo.SubmitJob(ctx, job)
		require.NoError(t, err)
		return got
	}
	require.Equal(t, []string{"swap"}, submit("a", "r1", "swap").SeriesTags)
	require.Equal(t, []string{"spot", "swap"}, submit("b", "r2", "spot").SeriesTags)
	require.Empty(t, submit("c", "r3").SeriesTags)
}

func TestBackfillNoticeKeepsSeriesTag(t *testing.T) {
	repo := openResampleStore(t).Resample()
	ctx := context.Background()
	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	require.NoError(t, repo.AddNotices(ctx, []BackfillNotice{
		{SpaceID: "crypto", DatasetID: "d", Frequency: "5m", SubjectID: "BTC", SeriesTag: "swap", Start: start, End: start.Add(time.Hour), Origin: "resample"},
		{SpaceID: "crypto", DatasetID: "d", Frequency: "5m", SubjectID: "ETH", Start: start, End: start.Add(time.Hour), Origin: "resample"},
	}))
	notices, err := repo.UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, notices, 2)
	require.Equal(t, "swap", notices[0].SeriesTag)
	require.Empty(t, notices[1].SeriesTag)
}
