package pebble_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
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
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:01:00+08:00"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
	}

	require.NoError(t, store.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))
	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, &pb.TimeRange{
		StartTime: "2026-06-15T00:00:00+08:00",
		EndTime:   "2026-06-15T00:01:00+08:00",
	}, "", "", []string{"close"}, nil)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got[0].GetColumns(), 1)
	require.Equal(t, "close", got[0].GetColumns()[0].GetColumnName())
}

func TestStoreReadsSubsecondRowsAfterWholeSecondLowerBound(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		SubjectId:  "APT-USDT",
		Freq:       "1m",
		Dimensions: map[string]string{"market": "spot"},
	}
	rows := []*pb.DataRow{
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-01-01T00:00:00.5Z", RowId: "half"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-01-01T00:00:01Z", RowId: "one"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
	}

	require.NoError(t, store.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))
	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, &pb.TimeRange{
		StartTime: "2026-01-01T00:00:00Z",
		EndTime:   "2026-01-01T00:00:01Z",
	}, "", "", []string{"close"}, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
	require.Equal(t, "half", got[0].GetKey().GetRowId())
	require.Equal(t, "one", got[1].GetKey().GetRowId())
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
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
		},
		{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"a": "b", "c": "d"}},
				DataTime: "2026-06-15T00:00:00+08:00",
				RowId:    "same-row",
			},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)},
		},
	}

	require.NoError(t, store.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))
	got, page, err := store.ReadRows(ctx, baseScope, pb.ReadMode_READ_MODE_RANGE, nil, "", "", []string{"close"}, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
	require.NotEqual(t, got[0].GetKey().GetScope().GetDimensions(), got[1].GetKey().GetScope().GetDimensions())
}

func TestStoreKeepsLegacyTimeSeriesRowsWithDifferentRowIDsSeparate(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "ticks", SubjectId: "APT-USDT", Freq: "tick"}
	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{
		{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "trade-1"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("price", 8.1)},
		},
		{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "trade-2"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("price", 8.2)},
		},
	}, pb.WriteMode_WRITE_MODE_UPSERT))

	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, &pb.TimeRange{
		StartTime: "2026-06-15T00:00:00Z",
		EndTime:   "2026-06-15T00:00:00Z",
	}, "", "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
	require.Equal(t, "trade-1", got[0].GetKey().GetRowId())
	require.Equal(t, "trade-2", got[1].GetKey().GetRowId())
}

func TestStoreMergesColumnsForSameDataKey(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	key := &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "bar-1"}

	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{{
		Key: key,
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("open", 8.0),
			testutil.DoubleValue("close", 8.1),
		},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{{
		Key:     key,
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, nil, "", "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Len(t, got, 1)
	values := columnDoubles(got[0])
	require.Equal(t, 8.0, values["open"])
	require.Equal(t, 8.2, values["close"])
}

func TestStoreOverwriteModeDoesNotDeleteOtherRowsInScope(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{
		{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "bar-1"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
		},
		{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:01:00Z", RowId: "bar-2"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)},
		},
	}, pb.WriteMode_WRITE_MODE_UPSERT))

	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{{
		Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "bar-1"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.3)},
	}}, pb.WriteMode_WRITE_MODE_OVERWRITE))

	got, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, nil, "", "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, got, 2)
	require.Equal(t, 8.3, columnDoubles(got[0])["close"])
	require.Equal(t, 8.2, columnDoubles(got[1])["close"])
}

func TestStoreConcurrentUpdatesMergeColumnsForSameDataKey(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	key := &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "bar-1"}
	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{{
		Key:     key,
		Columns: []*pb.ColumnValue{testutil.DoubleValue("base", 1)},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	const updates = 20
	var wg sync.WaitGroup
	errs := make(chan error, updates)
	for i := 0; i < updates; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.WriteRows(ctx, []*pb.DataRow{{
				Key:     key,
				Columns: []*pb.ColumnValue{testutil.DoubleValue(fmt.Sprintf("c%02d", i), float64(i))},
			}}, pb.WriteMode_WRITE_MODE_UPSERT)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	got, _, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, nil, "", "", nil, nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	values := columnDoubles(got[0])
	require.Equal(t, 1.0, values["base"])
	for i := 0; i < updates; i++ {
		require.Equal(t, float64(i), values[fmt.Sprintf("c%02d", i)])
	}
}

func TestStoreReadsNextCursorPageForExactScope(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		SubjectId:  "APT-USDT",
		Freq:       "1m",
		Dimensions: map[string]string{"market": "spot"},
	}
	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "bar-1"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:01:00Z", RowId: "bar-2"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:02:00Z", RowId: "bar-3"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.3)}},
	}, pb.WriteMode_WRITE_MODE_UPSERT))

	first, page, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, nil, "", "", nil, &pb.Page{Size: 2})
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.True(t, page.GetHasMore())
	require.NotEmpty(t, page.GetNextCursor())

	next, nextPage, err := store.ReadRows(ctx, scope, pb.ReadMode_READ_MODE_RANGE, nil, "", "", nil, &pb.Page{Size: 2, Cursor: page.GetNextCursor()})
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.False(t, nextPage.GetHasMore())
	require.Equal(t, "bar-3", next[0].GetKey().GetRowId())
}

func TestStoreCanDisableSyncWrites(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir(), DisableSyncWrites: true})
	require.NoError(t, err)
	defer store.Close()

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "APT-USDT"}
	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{{
		Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "symbol"},
		Columns: []*pb.ColumnValue{testutil.StringValue("status", "active")},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	got, page, err := store.ReadRows(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols"}, pb.ReadMode_READ_MODE_RANGE, nil, "", "symbol", nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Len(t, got, 1)
	require.Equal(t, "active", got[0].GetColumns()[0].GetValue().GetStringValue())
}

func TestStoreReadsObjectRowsByObjectIDPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer store.Close()

	objectScope := &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols"}
	timeSeriesScope := &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols", SubjectId: "AR-USDT", Freq: "1m"}
	require.NoError(t, store.WriteRows(ctx, []*pb.DataRow{
		{
			Key:     &pb.DataKey{Scope: objectScope, DataTime: "v1", RowId: "AR-USDT"},
			Columns: []*pb.ColumnValue{testutil.StringValue("status", "active")},
		},
		{
			Key:     &pb.DataKey{Scope: objectScope, DataTime: "v1", RowId: "APT-USDT"},
			Columns: []*pb.ColumnValue{testutil.StringValue("status", "inactive")},
		},
		{
			Key:     &pb.DataKey{Scope: timeSeriesScope, DataTime: "2026-06-15T00:00:00Z", RowId: "2026-06-15T00:00:00Z"},
			Columns: []*pb.ColumnValue{testutil.StringValue("status", "timeseries")},
		},
	}, pb.WriteMode_WRITE_MODE_UPSERT))

	got, page, err := store.ReadRows(ctx, &pb.DataScope{SpaceId: "crypto", DatasetId: "symbols"}, pb.ReadMode_READ_MODE_RANGE, nil, "", "AR-USDT", nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Len(t, got, 1)
	require.Equal(t, "AR-USDT", got[0].GetKey().GetRowId())
	require.Equal(t, "active", got[0].GetColumns()[0].GetValue().GetStringValue())
}

func columnDoubles(row *pb.DataRow) map[string]float64 {
	out := make(map[string]float64, len(row.GetColumns()))
	for _, column := range row.GetColumns() {
		out[column.GetColumnName()] = column.GetValue().GetDoubleValue()
	}
	return out
}
