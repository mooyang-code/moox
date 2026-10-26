package seriessync

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 每期拉取已经把新 K 线写进本地之后，变更日志里同一批新 K 线不应该再被比对性重拉（15.2 的长尾根因）。
func TestFollowSkipsChangesAlreadyReflectedByPull(t *testing.T) {
	st := newFakeStorage()
	subjects := []string{"A", "B", "C"}
	for _, subject := range subjects {
		st.put(subject, 100, 0, 1, 2, 3, 4)
	}
	s, spec := newSyncer(t, st)
	ctx := context.Background()
	_, err := s.Baseline(ctx, src)
	require.NoError(t, err)
	_, err = s.Pull(ctx, spec, subjects, at(5))
	require.NoError(t, err)

	// 新一期：采集器写入，随后拉取把它写进本地；变更日志里这三条都在拉取前的链头位置之内。
	for _, subject := range subjects {
		st.put(subject, 100, 5)
	}
	_, err = s.Pull(ctx, spec, subjects, at(6))
	require.NoError(t, err)

	before := st.reads
	res, err := s.Follow(ctx, spec)
	require.NoError(t, err)
	require.Empty(t, res.Applied)
	require.Equal(t, before, st.reads, "已被拉取反映的变更不应再请求 Storage")
	require.Equal(t, 3, res.Stats.Skipped)
	require.Equal(t, 0, res.Stats.Refetched)
	cur, ok, err := s.Cursor(src)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, st.head(), cur.Sequence)
	s.pulledMu.Lock()
	require.Empty(t, s.pulled[src], "位点越过链头位置后拉取记录应被清理")
	s.pulledMu.Unlock()
}

// 拉取之后才发生的修正（序号大于拉取前的链头位置）不能被跳过，否则会永久漏掉。
func TestFollowDoesNotSkipCorrectionsAfterPull(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2, 3, 4)
	s, spec := newSyncer(t, st)
	ctx := context.Background()
	_, err := s.Baseline(ctx, src)
	require.NoError(t, err)
	_, err = s.Pull(ctx, spec, []string{"A"}, at(5))
	require.NoError(t, err)

	st.put("A", 900, 4) // 拉取之后，同一根被修正
	res, err := s.Follow(ctx, spec)
	require.NoError(t, err)
	require.Equal(t, 0, res.Stats.Skipped)
	require.Equal(t, 1, res.Stats.Refetched)
	require.Equal(t, 904.0, closes(t, s, "A")[4])
}

// 必须重拉时按批请求：区间相同的标的合并，每批最多 BatchSubjects 个标的一次请求。
func TestFollowRefetchesInBatches(t *testing.T) {
	st := newFakeStorage()
	subjects := []string{"A", "B", "C", "D", "E"}
	for _, subject := range subjects {
		st.put(subject, 100, 0, 1, 2, 3, 4)
	}
	s, spec := newSyncer(t, st) // BatchSubjects = 2
	ctx := context.Background()
	_, err := s.Baseline(ctx, src)
	require.NoError(t, err)
	_, err = s.Pull(ctx, spec, subjects, at(5))
	require.NoError(t, err)
	for _, subject := range subjects {
		st.put(subject, 700, 2)
	}
	before := st.reads
	res, err := s.Follow(ctx, spec)
	require.NoError(t, err)
	require.Len(t, res.Applied, 5)
	require.Equal(t, 3, st.reads-before, "5 个标的、每批 2 个，应是 3 次窗口读取，而不是 5 次")
	require.Equal(t, 1+3, res.Stats.Requests)
	for _, subject := range subjects {
		require.Equal(t, 702.0, closes(t, s, subject)[2])
	}
}

// 被移出任务的标的：变更跟进不再处理它们的修正。
func TestFollowIgnoresSubjectsOutOfScope(t *testing.T) {
	st := newFakeStorage()
	for _, subject := range []string{"A", "B"} {
		st.put(subject, 100, 0, 1, 2, 3, 4)
	}
	s, spec := newSyncer(t, st)
	ctx := context.Background()
	_, err := s.Pull(ctx, spec, []string{"A", "B"}, at(5))
	require.NoError(t, err)
	_, err = s.Baseline(ctx, src)
	require.NoError(t, err)
	spec.Subjects = map[string]struct{}{"A": {}}

	st.put("A", 500, 2)
	st.put("B", 500, 2)
	res, err := s.Follow(ctx, spec)
	require.NoError(t, err)
	require.Len(t, res.Applied, 1)
	require.Equal(t, 1, res.Stats.OutOfScope)
	require.Equal(t, 502.0, closes(t, s, "A")[2])
	require.Equal(t, 102.0, closes(t, s, "B")[2], "移出任务的标的不再同步")
}
