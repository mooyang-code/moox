package datashard

import (
	"context"
	"testing"
	"time"

	contracts "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestOutboxConfigNormalizedAppliesDefaults(t *testing.T) {
	cfg := OutboxConfig{}.normalized()
	assert.Equal(t, 100, cfg.FlushBatchSize)
	assert.Equal(t, 1<<20, cfg.FlushMaxBytes)
	assert.Equal(t, 200*time.Millisecond, cfg.FlushInterval)
}

func TestOutboxRelayFlushPublishesAndDeletes(t *testing.T) {
	msg := testCommittedMessage(t, "node-1")
	store := &relayTestStore{
		entries: []*contracts.OutboxEntry{{Sequence: 1, Data: msg}},
	}
	publisher := &relayTestPublisher{}
	relay := NewOutboxRelay(store, publisher, OutboxConfig{FlushBatchSize: 10, FlushMaxBytes: 1 << 20})
	count, err := relay.flush(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, []uint64{1}, store.deleted)
	assert.NotZero(t, relay.LastAckTime())
}

func TestOutboxRelayFlushReturnsPublishError(t *testing.T) {
	msg := testCommittedMessage(t, "node-1")
	store := &relayTestStore{
		entries: []*contracts.OutboxEntry{{Sequence: 1, Data: msg}},
	}
	publisher := &relayTestPublisher{err: assert.AnError}
	relay := NewOutboxRelay(store, publisher, OutboxConfig{})
	_, err := relay.flush(context.Background())
	require.Error(t, err)
}

func TestOutboxRelayFlushStopsAtFirstFailureAndDeletesOnlyPrefix(t *testing.T) {
	msg := testCommittedMessage(t, "node-1")
	store := &relayTestStore{entries: []*contracts.OutboxEntry{
		{Sequence: 1, Data: msg},
		{Sequence: 2, Data: msg},
		{Sequence: 3, Data: msg},
	}}
	publisher := &sequencePublisher{errs: []error{nil, assert.AnError, nil}}
	relay := NewOutboxRelay(store, publisher, OutboxConfig{FlushBatchSize: 10})
	count, err := relay.flush(context.Background())
	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, count)
	assert.Equal(t, []uint64{1}, store.deleted)
	assert.Equal(t, 2, publisher.calls)
}

func TestOutboxRelayStartAndClose(t *testing.T) {
	store := &relayTestStore{}
	publisher := &relayTestPublisher{}
	relay := NewOutboxRelay(store, publisher, OutboxConfig{FlushInterval: 10 * time.Millisecond})
	relay.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	require.NoError(t, relay.Close())
}

type relayTestStore struct {
	entries []*contracts.OutboxEntry
	deleted []uint64
}

func (s *relayTestStore) Close() error                                    { return nil }
func (s *relayTestStore) WriteRows(context.Context, []*pb.ShardRow) error { return nil }
func (s *relayTestStore) WriteRowsWithOutbox(context.Context, []*pb.ShardRow, *contracts.OutboxEntry) error {
	return nil
}
func (s *relayTestStore) ListOutbox(context.Context, uint64, int, int) ([]*contracts.OutboxEntry, error) {
	return s.entries, nil
}
func (s *relayTestStore) DeleteOutbox(_ context.Context, seqs []uint64) error {
	s.deleted = append(s.deleted, seqs...)
	return nil
}
func (s *relayTestStore) ReadRows(context.Context, []*pb.ShardKey, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *relayTestStore) ScanRows(context.Context, *pb.ShardTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}

type relayTestPublisher struct {
	err error
}

type sequencePublisher struct {
	errs  []error
	calls int
}

func (p *sequencePublisher) PublishMessage(context.Context, []byte) error {
	idx := p.calls
	p.calls++
	if idx >= len(p.errs) {
		return nil
	}
	return p.errs[idx]
}

func (p *relayTestPublisher) PublishMessage(context.Context, []byte) error {
	return p.err
}

func testCommittedMessage(t *testing.T, shardID string) []byte {
	t.Helper()
	now := timestamppb.Now()
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{
		ShardId:   shardID,
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
	data, err := proto.Marshal(&messagepb.MooxMessage{
		ProtocolVersion: jetstream.ProtocolVersion,
		MessageId:       "msg-1",
		Topic:           "moox.storage.rows_committed.time_series.v1.mzxw6",
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-storage", InstanceId: shardID},
		Sequence:        1,
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/x-protobuf",
		MessageType:     "moox.storage.time_series.rows_committed.v1",
		Payload:         payload,
	})
	require.NoError(t, err)
	return data
}
