package primary

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocalClientWriteAndReadRows(t *testing.T) {
	store := &primaryTestStore{
		readRows: []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}},
	}
	client := NewLocalClient(LocalClientOptions{Pebble: store})
	target := &pb.PrimaryStoreTarget{NodeId: "node-1", Engine: "pebble"}
	rows := []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}}
	require.NoError(t, client.WriteRows(context.Background(), target, rows))
	got, page, err := client.ReadRows(context.Background(), target, &pb.ReadPrimaryRowsReq{Keys: []*pb.PrimaryStoreKey{{SpaceId: "crypto", DatasetId: "kline"}}})
	require.NoError(t, err)
	assert.Equal(t, store.readRows, got)
	assert.NotNil(t, page)
}

func TestLocalClientWriteRowsWithMessageUsesOutbox(t *testing.T) {
	store := &primaryTestStore{supportsOutbox: true}
	client := NewLocalClient(LocalClientOptions{Pebble: store})
	target := &pb.PrimaryStoreTarget{NodeId: "node-1"}
	msg := testOutboxMessage(t, "node-1")
	err := client.WriteRowsWithMessage(context.Background(), target, []*pb.PrimaryStoreRow{
		{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_"}},
	}, msg)
	require.Error(t, err)
	assert.Nil(t, store.outboxEntry)
}

func TestLocalClientRejectsUnsupportedEngine(t *testing.T) {
	client := NewLocalClient(LocalClientOptions{Pebble: &primaryTestStore{}})
	target := &pb.PrimaryStoreTarget{Engine: "mysql"}
	err := client.WriteRows(context.Background(), target, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported write engine")
}

func TestRejectOutboxBacklogEnforcesLimits(t *testing.T) {
	store := &primaryTestStore{
		outboxEntries: []*device.OutboxEntry{
			{Data: []byte("x"), CreatedAt: time.Now().UTC()},
			{Data: []byte("y"), CreatedAt: time.Now().UTC()},
		},
	}
	err := rejectOutboxBacklog(context.Background(), store, OutboxConfig{MaxRows: 1, MaxBytes: 1 << 20, MaxAge: time.Hour})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "row limit")
}

func TestLocalClientOpensSharedPebbleStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pebble", "main")
	clientA := NewLocalClient(LocalClientOptions{PebblePath: filepath.Dir(path)})
	clientB := NewLocalClient(LocalClientOptions{PebblePath: filepath.Dir(path)})
	target := &pb.PrimaryStoreTarget{Engine: "pebble"}
	row := testPrimaryTimeSeriesRow("2026-07-11T00:00:00.000000000Z")
	require.NoError(t, clientA.WriteRows(context.Background(), target, []*pb.PrimaryStoreRow{row}))
	got, _, err := clientB.ReadRows(context.Background(), target, &pb.ReadPrimaryRowsReq{
		Keys: []*pb.PrimaryStoreKey{row.GetKey()},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, clientA.Close())
	require.NoError(t, clientB.Close())
}

func TestLocalPebblePathUsesRoot(t *testing.T) {
	assert.Contains(t, localPebblePath("/tmp/root", ""), filepath.Join("/tmp/root", "pebble", "main"))
}

func testPrimaryTimeSeriesRow(version string) *pb.PrimaryStoreRow {
	return &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{
		SpaceId: "crypto", DatasetId: "binance_spot_kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		Key: fmt.Sprintf("BTC-USDT|1m|_"), Version: version,
	}}
}
