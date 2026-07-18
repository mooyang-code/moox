package primary

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxConfigNormalizedAppliesDefaults(t *testing.T) {
	cfg := OutboxConfig{}.normalized()
	assert.Equal(t, 100, cfg.FlushBatchSize)
	assert.Equal(t, 1<<20, cfg.FlushMaxBytes)
	assert.Equal(t, 200*time.Millisecond, cfg.FlushInterval)
}

func TestOutboxRelayFlushPublishesAndDeletes(t *testing.T) {
	msg := testOutboxMessage(t, "node-1")
	store := &relayTestStore{
		entries: []*device.OutboxEntry{{Sequence: 1, Data: msg}},
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
	msg := testOutboxMessage(t, "node-1")
	store := &relayTestStore{
		entries: []*device.OutboxEntry{{Sequence: 1, Data: msg}},
	}
	publisher := &relayTestPublisher{err: assert.AnError}
	relay := NewOutboxRelay(store, publisher, OutboxConfig{})
	_, err := relay.flush(context.Background())
	require.Error(t, err)
}

func TestOutboxRelayFlushStopsAtFirstFailureAndDeletesOnlyPrefix(t *testing.T) {
	msg := testOutboxMessage(t, "node-1")
	store := &relayTestStore{entries: []*device.OutboxEntry{
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
	entries []*device.OutboxEntry
	deleted []uint64
}

func (s *relayTestStore) Close() error                                           { return nil }
func (s *relayTestStore) WriteRows(context.Context, []*pb.PrimaryStoreRow) error { return nil }
func (s *relayTestStore) WriteRowsWithOutbox(context.Context, []*pb.PrimaryStoreRow, *device.OutboxEntry) error {
	return nil
}
func (s *relayTestStore) ListOutbox(context.Context, uint64, int, int) ([]*device.OutboxEntry, error) {
	return s.entries, nil
}
func (s *relayTestStore) DeleteOutbox(_ context.Context, seqs []uint64) error {
	s.deleted = append(s.deleted, seqs...)
	return nil
}
func (s *relayTestStore) ReadRows(context.Context, []*pb.PrimaryStoreKey, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *relayTestStore) ScanRows(context.Context, *pb.PrimaryStoreTarget, pb.DataKind, *pb.VersionRange, pb.SortOrder, []string, *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
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
