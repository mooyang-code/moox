package primary_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestLocalClientWritesToPebbleDevice(t *testing.T) {
	ctx := context.Background()
	facts, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer facts.Close()

	client := primary.NewLocalClient(primary.LocalClientOptions{Pebble: facts})
	row := primaryRow("2026-06-15T00:00:00Z")
	err = client.WriteRows(ctx, &pb.PrimaryStoreTarget{SpaceId: "crypto", NodeId: "local", Engine: "pebble", DatasetId: "kline"}, []*pb.PrimaryStoreRow{row})
	require.NoError(t, err)
}

func TestLocalClientRejectsUnsupportedEngine(t *testing.T) {
	ctx := context.Background()
	client := primary.NewLocalClient(primary.LocalClientOptions{Root: t.TempDir()})

	err := client.WriteRows(ctx, &pb.PrimaryStoreTarget{Engine: "duckdb"}, nil)
	require.ErrorContains(t, err, "unsupported write engine")

	_, _, err = client.ReadRows(ctx, &pb.PrimaryStoreTarget{Engine: "duckdb"}, &pb.ReadPrimaryRowsReq{})
	require.ErrorContains(t, err, "unsupported read engine")
}

func TestServiceImplementsPrimaryProtocol(t *testing.T) {
	ctx := context.Background()
	facts := testutil.OpenPebbleFactStore(t, t.TempDir())
	service := primary.NewService(primary.Options{Pebble: facts})
	target := &pb.PrimaryStoreTarget{SpaceId: "crypto", NodeId: "primary-1", DeviceId: "pebble-1", Engine: "pebble", DatasetId: "kline"}
	row := primaryRow("2026-06-15T00:00:00Z")

	writeRsp, err := service.WritePrimaryRows(ctx, &pb.WritePrimaryRowsReq{Target: target, Rows: []*pb.PrimaryStoreRow{row}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := service.ReadPrimaryRows(ctx, &pb.ReadPrimaryRowsReq{Target: target, Keys: []*pb.PrimaryStoreKey{row.GetKey()}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
}

func primaryRow(dataTime string) *pb.PrimaryStoreRow {
	normalized, err := factkey.NormalizeTimeVersion(dataTime)
	if err != nil {
		panic(err)
	}
	return &pb.PrimaryStoreRow{
		Key: &pb.PrimaryStoreKey{
			SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
			Key: factkey.BuildTimeSeriesDataKey("APT-USDT", "1m", nil), Version: normalized,
		},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}
}
