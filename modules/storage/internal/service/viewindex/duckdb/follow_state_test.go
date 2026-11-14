//go:build cgo

package duckdb

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func testRow(subject string, at time.Time, value float64) viewindex.RowWrite {
	return viewindex.RowWrite{
		Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: subject, Freq: "1m", DataTime: at.Format(time.RFC3339), SeriesTag: "venue:x",
		}}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}},
	}
}

// 跟随位置随行写入同一个事务落盘；单独落盘只前进不后退，换了存储（重建）直接覆盖，H_done 单独记录。
func TestFollowStatePersistsWithRowsAndOnlyAdvances(t *testing.T) {
	ctx := context.Background()
	m, base := deleteOldestTestIndex(t)

	stats, err := m.StatMetadata(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, viewindex.FollowState{}, stats.Follow, "新建的索引没有跟随进度")
	require.Equal(t, "2026-11-01 00:00:00", stats.ViewCreatedAt, "建索引时记下 View 的创建时间")

	require.NoError(t, m.SaveFollowState(ctx, "idx", viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store-a", Sequence: 10}, BuiltSequence: 15}))
	write := func(seq uint64) {
		require.NoError(t, m.Write(ctx, "idx", viewindex.ViewIndexWriteBatch{
			RowWrites: []viewindex.RowWrite{testRow("A", base, 1)}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
			Cursor: &viewindex.ChangeCursor{StoreID: "store-a", Sequence: seq},
		}))
	}
	write(20)
	stats, err = m.StatMetadata(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store-a", Sequence: 20}, BuiltSequence: 15}, stats.Follow)

	write(18)
	require.NoError(t, m.SaveFollowState(ctx, "idx", viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store-a", Sequence: 12}}))
	stats, err = m.Stat(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, uint64(20), stats.Follow.Cursor.Sequence, "较旧的位置不能盖掉较新的")
	require.Equal(t, uint64(15), stats.Follow.BuiltSequence, "只落盘游标时保留 H_done")

	require.NoError(t, m.SaveFollowState(ctx, "idx", viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store-b", Sequence: 3}, BuiltSequence: 4}))
	stats, err = m.StatMetadata(ctx, "idx")
	require.NoError(t, err)
	require.Equal(t, viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store-b", Sequence: 3}, BuiltSequence: 4}, stats.Follow)
}
