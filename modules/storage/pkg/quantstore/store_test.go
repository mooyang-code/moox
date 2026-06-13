package quantstore

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
	"github.com/stretchr/testify/require"
)

func TestStoreSetAndScanTimeSeries(t *testing.T) {
	store := New(t.TempDir())
	ref := &pb.DataRef{WorkspaceId: "default", DatasetId: "kline", InstrumentId: "APT-USDT", ExchangeId: "BINANCE", Freq: "1m"}

	affected, err := store.SetTimeSeries(context.Background(), []*pb.TimeSeriesPoint{
		{DataRef: ref, Time: "2026-01-01 00:00:00", Fields: []*pb.FieldValue{DoubleValue("close", 10.5)}},
		{DataRef: ref, Time: "2026-01-01 00:01:00", Fields: []*pb.FieldValue{DoubleValue("close", 11.5)}},
	}, pb.WriteMode_WRITE_MODE_APPEND)
	require.NoError(t, err)
	require.Equal(t, uint64(2), affected)

	rows, page, err := store.ScanTimeSeries(context.Background(), ref, &pb.TimeRange{StartTime: "2026-01-01 00:01:00", StartInclusive: true}, []string{"close"}, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), page.Total)
	require.Len(t, rows, 1)
	require.Equal(t, "2026-01-01 00:01:00", rows[0].GetTime())
	require.Equal(t, "close", rows[0].GetFields()[0].GetFieldName())
}

func TestLatestSnapshot(t *testing.T) {
	store := New(t.TempDir())
	ref := &pb.DataRef{WorkspaceId: "default", DatasetId: "kline", InstrumentId: "AR-USDT", ExchangeId: "BINANCE", Freq: "1m"}
	_, err := store.SetTimeSeries(context.Background(), []*pb.TimeSeriesPoint{
		{DataRef: ref, Time: "2026-01-01 00:00:00", Fields: []*pb.FieldValue{DoubleValue("close", 20)}},
		{DataRef: ref, Time: "2026-01-01 00:01:00", Fields: []*pb.FieldValue{DoubleValue("close", 21)}},
	}, pb.WriteMode_WRITE_MODE_APPEND)
	require.NoError(t, err)

	rows, err := store.LatestSnapshot(context.Background(), []*pb.DataRef{ref}, []string{"close"}, "2026-01-01 00:00:30")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "2026-01-01 00:00:00", rows[0].GetSnapshotTime())
}
