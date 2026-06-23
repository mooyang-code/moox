package pebble

import (
	"context"
	"testing"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStoreScanRowsCursorPageDoesNotDecodeRowsBeyondRequestedPage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	first := testTimeSeriesStoreKey("BTC-USDT", "1h", "2026-06-15T00:00:00Z")
	second := testTimeSeriesStoreKey("BTC-USDT", "1h", "2026-06-15T01:00:00Z")
	corrupt := testTimeSeriesStoreKey("BTC-USDT", "1h", "2026-06-15T02:00:00Z")
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{
		{Key: first, Columns: []*pb.ColumnValue{testDoubleColumn("close", 1)}},
		{Key: second, Columns: []*pb.ColumnValue{testDoubleColumn("close", 2)}},
	}))
	require.NoError(t, store.db.Set([]byte(encodePrimaryStoreKey(corrupt)), []byte("not a proto row"), cpebble.NoSync))

	rows, page, err := store.ScanRows(ctx,
		&pb.PrimaryStoreTarget{SpaceId: "crypto", DatasetId: "kline"},
		pb.DataKind_DATA_KIND_TIME_SERIES,
		nil,
		pb.SortOrder_SORT_ORDER_ASC,
		nil,
		&pb.Page{Size: 1, Cursor: encodePrimaryStoreKey(first)},
	)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, second.GetVersion(), rows[0].GetKey().GetVersion())
	require.True(t, page.GetHasMore())
	require.Equal(t, encodePrimaryStoreKey(second), page.GetNextCursor())
}

func testDoubleColumn(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func testTimeSeriesStoreKey(subjectID string, freq string, dataTime string) *pb.PrimaryStoreKey {
	normalized, err := factkey.NormalizeTimeVersion(dataTime)
	if err != nil {
		panic(err)
	}
	return &pb.PrimaryStoreKey{
		SpaceId:   "crypto",
		DatasetId: "kline",
		DataKind:  pb.DataKind_DATA_KIND_TIME_SERIES,
		Key:       factkey.BuildTimeSeriesDataKey(subjectID, freq, nil),
		Version:   normalized,
	}
}
