package adapter_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/services/adapter"
	"github.com/mooyang-code/moox/modules/storage/internal/services/device/pebble"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestLocalAdapterWritesToPebbleDevice(t *testing.T) {
	ctx := context.Background()
	facts, err := pebble.Open(pebble.Options{Path: t.TempDir()})
	require.NoError(t, err)
	defer facts.Close()

	client := adapter.NewLocal(adapter.LocalOptions{Pebble: facts})

	scope := &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}
	row := &pb.DataRow{
		Key:     &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00+08:00"},
		Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
	}
	err = client.WriteRows(ctx, &pb.DeviceRef{SpaceId: "crypto", NodeId: "local", Engine: "pebble", DatasetId: "kline"}, []*pb.DataRow{row}, pb.WriteMode_WRITE_MODE_UPSERT)
	require.NoError(t, err)
}

func TestLocalClientRejectsUnsupportedEngine(t *testing.T) {
	ctx := context.Background()
	client := adapter.NewLocalClient(quantstore.New(t.TempDir()))

	err := client.WriteRows(ctx, &pb.DeviceRef{Engine: "duckdb"}, nil, pb.WriteMode_WRITE_MODE_UPSERT)
	require.ErrorContains(t, err, "unsupported write engine")

	_, _, err = client.ReadRows(ctx, &pb.DeviceRef{Engine: "duckdb"}, &pb.ReadRowsReq{})
	require.ErrorContains(t, err, "unsupported read engine")
}
