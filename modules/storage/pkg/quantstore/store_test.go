package quantstore

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestStoreWriteAndReadRows(t *testing.T) {
	store := New(t.TempDir())
	scope := &pb.DataScope{SpaceId: "default", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}

	err := store.WriteRows(context.Background(), []*pb.DataRow{
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-01-01 00:00:00"}, Columns: []*pb.ColumnValue{DoubleValue("close", 10.5)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-01-01 00:01:00"}, Columns: []*pb.ColumnValue{DoubleValue("close", 11.5), DoubleValue("volume", 100)}},
	}, pb.WriteMode_WRITE_MODE_APPEND)
	require.NoError(t, err)

	rows, page, err := store.ReadRows(
		context.Background(),
		scope,
		pb.ReadMode_READ_MODE_RANGE,
		&pb.TimeRange{StartTime: "2026-01-01 00:01:00", StartInclusive: true},
		"",
		nil,
		[]string{"close"},
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Len(t, rows, 1)
	require.Equal(t, "2026-01-01 00:01:00", rows[0].GetKey().GetDataTime())
	require.Len(t, rows[0].GetColumns(), 1)
	require.Equal(t, "close", rows[0].GetColumns()[0].GetColumnName())
}

func TestReadRowsLatestBefore(t *testing.T) {
	store := New(t.TempDir())
	scope := &pb.DataScope{SpaceId: "default", DatasetId: "kline", SubjectId: "AR-USDT", Freq: "1m"}
	err := store.WriteRows(context.Background(), []*pb.DataRow{
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-01-01 00:00:00"}, Columns: []*pb.ColumnValue{DoubleValue("close", 20)}},
		{Key: &pb.DataKey{Scope: scope, DataTime: "2026-01-01 00:01:00"}, Columns: []*pb.ColumnValue{DoubleValue("close", 21)}},
	}, pb.WriteMode_WRITE_MODE_APPEND)
	require.NoError(t, err)

	rows, _, err := store.ReadRows(
		context.Background(),
		scope,
		pb.ReadMode_READ_MODE_LATEST_BEFORE,
		nil,
		"2026-01-01 00:00:30",
		nil,
		[]string{"close"},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "2026-01-01 00:00:00", rows[0].GetKey().GetDataTime())
}

func TestReadRowsCanScanDatasetWithoutSubject(t *testing.T) {
	store := New(t.TempDir())
	rows := []*pb.DataRow{
		{
			Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols", SubjectId: "APT-USDT"}, RowId: "APT-USDT"},
			Columns: []*pb.ColumnValue{StringValue("symbol", "APTUSDT")},
		},
		{
			Key:     &pb.DataKey{Scope: &pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols", SubjectId: "AR-USDT"}, RowId: "AR-USDT"},
			Columns: []*pb.ColumnValue{StringValue("symbol", "ARUSDT")},
		},
	}
	err := store.WriteRows(context.Background(), rows, pb.WriteMode_WRITE_MODE_UPSERT)
	require.NoError(t, err)

	got, page, err := store.ReadRows(
		context.Background(),
		&pb.DataScope{SpaceId: "default", DatasetId: "binance_spot_symbols"},
		pb.ReadMode_READ_MODE_RANGE,
		nil,
		"",
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, uint64(2), page.GetTotal())
	require.ElementsMatch(t, []string{"APT-USDT", "AR-USDT"}, []string{got[0].GetKey().GetRowId(), got[1].GetKey().GetRowId()})
}
