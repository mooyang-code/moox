package pebble

import (
	"context"
	"path/filepath"
	"testing"

	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWriteRowsWithOutboxStagesMessage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	row := testPrimaryTimeSeriesRow("2026-07-11T00:00:00.000000000Z")
	entry := &contracts.OutboxEntry{Data: testOutboxMessageBytes(t)}
	require.NoError(t, store.WriteRowsWithOutbox(ctx, []*pb.ShardRow{row}, entry))
	assert.NotZero(t, entry.Sequence)

	entries, err := store.ListOutbox(ctx, 0, 10, 1<<20)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, entry.Sequence, entries[0].Sequence)

	require.NoError(t, store.DeleteOutbox(ctx, []uint64{entries[0].Sequence}))
	entries, err = store.ListOutbox(ctx, 0, 10, 1<<20)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestWriteRowsWithOutboxRejectsInvalidMessage(t *testing.T) {
	store, err := Open(Options{ShardID: "test-shard", Path: filepath.Join(t.TempDir(), "primary")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	err = store.WriteRowsWithOutbox(context.Background(), nil, &contracts.OutboxEntry{Data: []byte("bad")})
	require.Error(t, err)
}

func TestOutboxKeyFormatsSequence(t *testing.T) {
	assert.Contains(t, outboxKey(42), "00000000000000000042")
}

func testOutboxMessageBytes(t *testing.T) []byte {
	t.Helper()
	now := timestamppb.Now()
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{
		ShardId:   "node-1",
		SpaceId:   "crypto",
		DatasetId: "kline",
		Writes: []*pb.TimeSeriesRowWrite{{
			Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE,
			Row: &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{
				SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-11T00:00:00Z",
			}},
		}},
	})
	require.NoError(t, err)
	msg := &messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       "msg-1",
		Topic:           "moox.storage.rows_committed.time_series.v1.mzxw6",
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-storage", InstanceId: "node-1"},
		Sequence:        1,
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/x-protobuf",
		MessageType:     "moox.storage.time_series.rows_committed.v1",
		Payload:         payload,
	}
	data, err := proto.Marshal(msg)
	require.NoError(t, err)
	return data
}
