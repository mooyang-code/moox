package pebble_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStoreWritesAndReadsRowsByTimeRange(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	rows := []*pb.DataRow{
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"}, Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:01:00+08:00"}, Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.2)}},
	}

	require.NoError(t, store.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))
	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, &pb.TimeRange{
		StartTime: "2026-06-15T00:00:00+08:00",
		EndTime:   "2026-06-15T00:01:00+08:00",
	}, "", nil, []string{"close"}, nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got[0].GetColumns(), 1)
	require.Equal(t, "close", got[0].GetColumns()[0].GetColumnName())
}

func TestStoreKeepsRowsWithAmbiguousDimensionTextSeparate(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	baseScope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	rows := []*pb.DataRow{
		{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"a": "b&c=d"}},
				DataTime: "2026-06-15T00:00:00+08:00",
				RowId:    "same-row",
			},
			Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
		},
		{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"a": "b", "c": "d"}},
				DataTime: "2026-06-15T00:00:00+08:00",
				RowId:    "same-row",
			},
			Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.2)},
		},
	}

	require.NoError(t, store.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))
	got, page, err := store.ReadRows(ctx, baseScope, pb.ReadMode_READ_MODE_RANGE, nil, "", nil, []string{"close"}, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
	require.NotEqual(t, got[0].GetKey().GetScope().GetDimensions(), got[1].GetKey().GetScope().GetDimensions())
}
