package pebble

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestWriteRowsMergesExistingRowColumns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	base := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	base.Columns = []*pb.ColumnValue{
		{ColumnName: "open", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}},
		{ColumnName: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}}},
	}
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{base}))

	patch := testPrimaryTimeSeriesRow("2026-07-09T08:10:00.000000000Z")
	patch.Columns = []*pb.ColumnValue{
		{ColumnName: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 3}}},
		{ColumnName: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}},
	}
	require.NoError(t, store.WriteRows(ctx, []*pb.PrimaryStoreRow{patch}))

	rows, _, err := store.ReadRows(ctx, []*pb.PrimaryStoreKey{testPrimaryTimeSeriesKey("2026-07-09T08:10:00.000000000Z")}, nil, pb.SortOrder_SORT_ORDER_ASC, nil, nil)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	columns := map[string]float64{}
	for _, col := range rows[0].GetColumns() {
		if num := col.GetValue().GetDoubleValue(); num != 0 {
			columns[col.GetColumnName()] = num
		}
	}
	require.Equal(t, float64(1), columns["open"])
	require.Equal(t, float64(3), columns["close"])
	require.Equal(t, float64(100), columns["volume"])
}
