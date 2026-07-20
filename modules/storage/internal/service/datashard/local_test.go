//go:build legacy_storage

package datashard

import (
	"context"
	"fmt"
	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalClientWriteAndReadRows(t *testing.T) {
	store := &primaryTestStore{
		readRows: []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}},
	}
	client := NewLocalClient(LocalClientOptions{Pebble: store})
	target := &pb.ShardTarget{NodeId: "node-1", ShardId: "test-shard", Engine: "pebble"}
	rows := []*pb.ShardRow{{Key: &pb.ShardKey{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}}
	require.NoError(t, client.WriteRows(context.Background(), target, rows))
	got, page, err := client.ReadRows(context.Background(), target, &pb.ReadRowsReq{Keys: []*pb.ShardKey{{SpaceId: "crypto", DatasetId: "kline"}}})
	require.NoError(t, err)
	assert.Equal(t, store.readRows, got)
	assert.NotNil(t, page)
}

func TestLocalClientRejectsUnsupportedEngine(t *testing.T) {
	client := NewLocalClient(LocalClientOptions{Pebble: &primaryTestStore{}})
	target := &pb.ShardTarget{Engine: "mysql"}
	err := client.WriteRows(context.Background(), target, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported write engine")
}

func TestRejectOutboxBacklogEnforcesLimits(t *testing.T) {
	store := &primaryTestStore{
		outboxEntries: []*contracts.OutboxEntry{
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
	clientA := NewLocalClient(LocalClientOptions{PebblePath: filepath.Dir(path), ShardID: "test-shard"})
	clientB := NewLocalClient(LocalClientOptions{PebblePath: filepath.Dir(path), ShardID: "test-shard"})
	target := &pb.ShardTarget{Engine: "pebble", NodeId: "test-shard", ShardId: "test-shard"}
	row := testPrimaryTimeSeriesRow("2026-07-11T00:00:00.000000000Z")
	require.NoError(t, clientA.WriteRows(context.Background(), target, []*pb.ShardRow{row}))
	got, _, err := clientB.ReadRows(context.Background(), target, &pb.ReadRowsReq{
		Keys: []*pb.ShardKey{row.GetKey()},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, clientA.Close())
	require.NoError(t, clientB.Close())
}

func TestLocalPebblePathUsesRoot(t *testing.T) {
	assert.Contains(t, localPebblePath("/tmp/root", ""), filepath.Join("/tmp/root", "pebble", "main"))
}

func testPrimaryTimeSeriesRow(version string) *pb.ShardRow {
	return &pb.ShardRow{Key: &pb.ShardKey{
		SpaceId: "crypto", DatasetId: "binance_spot_kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		Key: fmt.Sprintf("BTC-USDT|1m|_"), Version: version,
	}}
}
