package primary_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/services/primary"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestLocalPrimaryWritesToPebbleDevice(t *testing.T) {
	ctx := context.Background()
	facts, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer facts.Close()

	client := primary.NewLocal(primary.LocalOptions{Pebble: facts})

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	row := &pb.DataRow{
		Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}
	err = client.WriteRows(ctx, &pb.PrimaryTarget{SpaceId: "crypto", NodeId: "local", Engine: "pebble", DatasetId: "kline"}, []*pb.DataRow{row}, pb.WriteMode_WRITE_MODE_UPSERT)
	require.NoError(t, err)
}

func TestLocalClientRejectsUnsupportedEngine(t *testing.T) {
	ctx := context.Background()
	client := primary.NewLocalClient(primary.LocalClientOptions{Root: t.TempDir()})

	err := client.WriteRows(ctx, &pb.PrimaryTarget{Engine: "duckdb"}, nil, pb.WriteMode_WRITE_MODE_UPSERT)
	require.ErrorContains(t, err, "unsupported write engine")

	_, _, err = client.ReadRows(ctx, &pb.PrimaryTarget{Engine: "duckdb"}, &pb.ReadRowsReq{})
	require.ErrorContains(t, err, "unsupported read engine")
}

func TestServiceImplementsPrimaryProtocol(t *testing.T) {
	ctx := context.Background()
	facts := testutil.OpenPebbleFactStore(t, t.TempDir())
	service := primary.NewService(primary.Options{Pebble: facts})

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	writeRsp, err := service.WritePrimaryRows(ctx, &pb.WritePrimaryRowsReq{
		Target:    &pb.PrimaryTarget{SpaceId: "crypto", NodeId: "primary-1", DeviceId: "pebble-1", Engine: "pebble", DatasetId: "kline"},
		WriteMode: pb.WriteMode_WRITE_MODE_UPSERT,
		Rows: []*pb.DataRow{{
			Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"},
			Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, writeRsp.GetRetInfo().GetCode())

	readRsp, err := service.ReadPrimaryRows(ctx, &pb.ReadPrimaryRowsReq{
		Target:    &pb.PrimaryTarget{SpaceId: "crypto", NodeId: "primary-1", DeviceId: "pebble-1", Engine: "pebble", DatasetId: "kline"},
		Scope:     scope,
		ReadMode:  pb.ReadMode_READ_MODE_RANGE,
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:00:00+08:00"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode())
	require.Len(t, readRsp.GetRows(), 1)
}
