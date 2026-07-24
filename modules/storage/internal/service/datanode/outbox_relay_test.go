package datanode

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

type testPublisher struct {
	values   [][]byte
	attempts [][]byte
	calls    int
	failAt   int
}

func (p *testPublisher) PublishMessage(_ context.Context, data []byte) error {
	p.calls++
	p.attempts = append(p.attempts, append([]byte(nil), data...))
	if p.failAt > 0 && p.calls == p.failAt {
		return errors.New("publish failed")
	}
	p.values = append(p.values, append([]byte(nil), data...))
	return nil
}

type duplicatePublisher struct{ *testPublisher }

func (p *duplicatePublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if err := p.PublishMessage(ctx, data); err != nil {
		return nil, err
	}
	return &jetstream.PublishAck{Duplicate: true}, nil
}

type retryDuplicatePublisher struct {
	testPublisher
}

func (p *retryDuplicatePublisher) PublishMessageWithAck(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if err := p.PublishMessage(ctx, data); err != nil {
		return nil, err
	}
	return &jetstream.PublishAck{Duplicate: p.calls > 1}, nil
}

func TestRelayStopsAtFailedEntryAndRetriesIt(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	_, err = store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{failAt: 2}
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewOutboxRelay(store, publisher, OutboxRelayOptions{BatchSize: 10, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Flush(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if got := metrics.Snapshot().OutboxPublishErrorsTotal; got != 1 {
		t.Fatalf("publish errors = %d, want 1", got)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 || entries[0].ID != 2 {
		t.Fatalf("remaining outbox=%v err=%v", entries, err)
	}
	publisher.failAt = 0
	if err := relay.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.attempts) != 3 || !bytes.Equal(publisher.attempts[1], publisher.attempts[2]) {
		t.Fatalf("retry attempts = %d, bytes stable = %t", len(publisher.attempts), len(publisher.attempts) == 3 && bytes.Equal(publisher.attempts[1], publisher.attempts[2]))
	}
	failedAttempt := &eventpb.EventMessage{}
	retryAttempt := &eventpb.EventMessage{}
	if err := proto.Unmarshal(publisher.attempts[1], failedAttempt); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(publisher.attempts[2], retryAttempt); err != nil {
		t.Fatal(err)
	}
	if failedAttempt.GetEventId() == "" || failedAttempt.GetEventId() != retryAttempt.GetEventId() {
		t.Fatalf("EventID changed across retry: %q vs %q", failedAttempt.GetEventId(), retryAttempt.GetEventId())
	}
	entries, err = store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("remaining after retry=%v err=%v", entries, err)
	}
}

func TestRelayRecordsOutboxSnapshotAndDuplicateAcknowledgement(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewOutboxRelay(store, &duplicatePublisher{testPublisher: &testPublisher{}}, OutboxRelayOptions{Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.Snapshot()
	if snapshot.OutboxPendingEntries != 0 || snapshot.OutboxOldestAge != 0 {
		t.Fatalf("outbox snapshot after flush = %+v", snapshot)
	}
	expected := "# HELP moox_storage_outbox_duplicate_publish_total Storage outbox publishes acknowledged as duplicates.\n# TYPE moox_storage_outbox_duplicate_publish_total counter\nmoox_storage_outbox_duplicate_publish_total 1\n"
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "moox_storage_outbox_duplicate_publish_total"); err != nil {
		t.Fatal(err)
	}
}

func TestRelayRecoversWhenDeleteFailsAfterPublishAndRestarts(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "db")
	store, err := pebble.Open(pebble.Options{Path: storePath, NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	publisher := &retryDuplicatePublisher{}
	deleteErr := errors.New("simulated outbox delete failure")
	firstDelete := true
	firstMetrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	firstStore := store
	firstRelay, err := NewOutboxRelay(firstStore, publisher, OutboxRelayOptions{Metrics: firstMetrics, DeleteOutbox: func(ctx context.Context, ids []uint64) error {
		if firstDelete {
			firstDelete = false
			return deleteErr
		}
		return firstStore.DeleteOutbox(ctx, ids)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstRelay.Flush(context.Background()); !errors.Is(err, deleteErr) {
		t.Fatalf("first flush error=%v, want delete failure", err)
	}
	entries, err := firstStore.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox after failed delete=%v err=%v", entries, err)
	}

	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = pebble.Open(pebble.Options{Path: storePath, NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	secondMetrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	secondRelay, err := NewOutboxRelay(store, publisher, OutboxRelayOptions{Metrics: secondMetrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondRelay.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err = store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outbox after restart=%v err=%v", entries, err)
	}
	if publisher.calls != 2 || secondMetrics.Snapshot().OutboxDuplicatePublishTotal != 1 {
		t.Fatalf("calls=%d duplicate_metrics=%d, want one replay duplicate", publisher.calls, secondMetrics.Snapshot().OutboxDuplicatePublishTotal)
	}
}
