package pebble

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func openChangeLogStore(t *testing.T, retention time.Duration) *Store {
	t.Helper()
	s, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", ChangeLogRetention: retention})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestChangeLogRecordsWritesInOrder(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	head, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", FromHead: true})
	require.NoError(t, err)
	require.Equal(t, uint64(0), head.GetNextCursor().GetSequence())
	require.Empty(t, head.GetChanges())

	// 一次写入里同一标的的多行合并成一个时间范围，两个数据集各记一条。
	require.NoError(t, s.UpsertFields(ctx, []*pb.RowFieldUpsert{
		tsRow("BTCUSDT", "", base, 1), tsRow("BTCUSDT", "", base.Add(2*time.Minute), 2), tsRow("ETHUSDT", "", base.Add(time.Minute), 3),
	}))
	other := tsRow("BTCUSDT", "", base, 9)
	other.Key.DatasetId = "other"
	require.NoError(t, s.UpsertFields(ctx, []*pb.RowFieldUpsert{other}))

	got, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: head.GetNextCursor()})
	require.NoError(t, err)
	require.False(t, got.GetGap())
	require.Len(t, got.GetChanges(), 1, "只返回请求的数据集")
	change := got.GetChanges()[0]
	require.Equal(t, uint64(1), change.GetSequence())
	require.Len(t, change.GetRanges(), 2)
	require.Equal(t, "BTCUSDT", change.GetRanges()[0].GetSubjectId())
	require.Equal(t, "2026-10-01T00:00:00Z", change.GetRanges()[0].GetStartTime())
	require.Equal(t, "2026-10-01T00:02:00Z", change.GetRanges()[0].GetEndTime())
	require.Equal(t, "ETHUSDT", change.GetRanges()[1].GetSubjectId())
	// 游标按该节点的全部变更推进（包括别的数据集的序号 2）。
	require.Equal(t, uint64(2), got.GetNextCursor().GetSequence())

	// 从新游标继续读，没有新变更。
	again, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: got.GetNextCursor()})
	require.NoError(t, err)
	require.Empty(t, again.GetChanges())
	require.False(t, again.GetGap())
	require.Equal(t, uint64(2), again.GetNextCursor().GetSequence())
}

func TestChangeLogPagingAndHasMore(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		require.NoError(t, s.UpsertFields(ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}))
	}
	cursor := &pb.DatasetChangeCursor{SourceNodeId: "node-1", SourceStoreId: mustHead(t, s).GetSourceStoreId()}
	var seen []uint64
	for {
		rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: cursor, Limit: 2})
		require.NoError(t, err)
		for _, c := range rsp.GetChanges() {
			seen = append(seen, c.GetSequence())
		}
		cursor = rsp.GetNextCursor()
		if !rsp.GetHasMore() {
			break
		}
	}
	require.Equal(t, []uint64{1, 2, 3, 4, 5}, seen)
}

func mustHead(t *testing.T, s *Store) *pb.DatasetChangeCursor {
	t.Helper()
	head, err := s.ChangeCursor()
	require.NoError(t, err)
	return head
}

func TestChangeLogGapDetection(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, time.Hour)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		require.NoError(t, s.UpsertFields(ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(time.Duration(i)*time.Minute), float64(i))}))
	}
	head := mustHead(t, s)
	start := &pb.DatasetChangeCursor{SourceNodeId: head.GetSourceNodeId(), SourceStoreId: head.GetSourceStoreId(), Sequence: 0}

	// 清理掉前两条（把 cutoff 放到未来，全部过期后再写新的）。
	removed, err := s.CleanupChangeLog(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 4, removed)
	require.NoError(t, s.UpsertFields(ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base.Add(10*time.Minute), 10)}))

	// 游标 0 早于清理到的序号 4：断档。
	rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: start})
	require.NoError(t, err)
	require.True(t, rsp.GetGap())

	// 游标正好在清理边界上：没有断档，读到之后的新变更。
	edge := &pb.DatasetChangeCursor{SourceNodeId: head.GetSourceNodeId(), SourceStoreId: head.GetSourceStoreId(), Sequence: 4}
	rsp, err = s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: edge})
	require.NoError(t, err)
	require.False(t, rsp.GetGap())
	require.Len(t, rsp.GetChanges(), 1)
	require.Equal(t, uint64(5), rsp.GetChanges()[0].GetSequence())

	// 身份对不上（存储重建或节点更换）：断档。
	rsp, err = s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{SourceNodeId: "node-2", SourceStoreId: head.GetSourceStoreId(), Sequence: 5}})
	require.NoError(t, err)
	require.True(t, rsp.GetGap())
	// 比头部还新：断档。
	rsp, err = s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{SourceNodeId: "node-1", SourceStoreId: head.GetSourceStoreId(), Sequence: 99}})
	require.NoError(t, err)
	require.True(t, rsp.GetGap())
}

func TestChangeLogCleanupKeepsRecentEntries(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 7*24*time.Hour)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.UpsertFields(ctx, []*pb.RowFieldUpsert{tsRow("BTCUSDT", "", base, 1)}))
	removed, err := s.CleanupChangeLog(ctx, time.Now().Add(-s.ChangeLogRetention()))
	require.NoError(t, err)
	require.Equal(t, 0, removed)
	rsp, err := s.ReadDatasetChanges(ctx, &pb.ReadDatasetChangesReq{SpaceId: "s", DatasetId: "d", Cursor: &pb.DatasetChangeCursor{SourceNodeId: "node-1", SourceStoreId: mustHead(t, s).GetSourceStoreId()}})
	require.NoError(t, err)
	require.Len(t, rsp.GetChanges(), 1)
}

func TestFactorResultWritesAreNotChangeLogged(t *testing.T) {
	ctx := context.Background()
	s := openChangeLogStore(t, 0)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	row := tsRow("BTCUSDT", "", base, 1)
	row.Key.DatasetId = "factor_d"
	_, err := s.WriteFactorRows(ctx, "s", "factor_d", "commit-1", []*pb.RowFieldUpsert{row})
	require.NoError(t, err)
	require.Equal(t, uint64(0), mustHead(t, s).GetSequence(), "因子结果不是本地副本的输入")
}
