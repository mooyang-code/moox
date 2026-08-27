package outbox

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type blockingPublisher struct{}

func (blockingPublisher) PublishMessage(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
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

type reconnectPublisher struct {
	values       [][]byte
	unavailable  bool
	reconnects   int
	reconnectErr error
}

func (p *reconnectPublisher) PublishMessage(_ context.Context, data []byte) error {
	if p.unavailable {
		return errors.New("eventbus connection is stale")
	}
	p.values = append(p.values, append([]byte(nil), data...))
	return nil
}

func (p *reconnectPublisher) Reconnect(context.Context) error {
	p.reconnects++
	if p.reconnectErr != nil {
		return p.reconnectErr
	}
	p.unavailable = false
	return nil
}

func (p *reconnectPublisher) Ready() bool { return !p.unavailable }

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
	_, err = store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
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
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	if got := metrics.Snapshot().OutboxPublishErrorsTotal; got != 1 {
		t.Fatalf("publish errors = %d, want 1", got)
	}
	failedSnapshot := metrics.Snapshot()
	if !failedSnapshot.OutboxObserved || failedSnapshot.OutboxPendingEntries != 1 {
		t.Fatalf("outbox state after publish failure = %+v, want one observable pending entry", failedSnapshot)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 || entries[0].ID != 2 {
		t.Fatalf("remaining outbox=%v err=%v", entries, err)
	}
	publisher.failAt = 0
	if err := relay.flush(context.Background()); err != nil {
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

func TestRelayBoundsPublishAttempt(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(store, blockingPublisher{}, RelayOptions{PublishTimeout: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := relay.flush(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("flush error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("publish attempt took %s, want bounded", elapsed)
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("outbox after timeout=%v err=%v", entries, err)
	}
}

func TestRelayReconnectsPublisherAfterRepeatedFailures(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	publisher := &reconnectPublisher{unavailable: true}
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(store, publisher, RelayOptions{ReconnectAfterFailures: 3, ReconnectCooldown: time.Nanosecond, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := relay.flush(context.Background()); err == nil {
			t.Fatal("expected stale publisher failure")
		}
	}
	if publisher.reconnects != 1 {
		t.Fatalf("reconnects=%d, want 1", publisher.reconnects)
	}
	afterReconnect := metrics.Snapshot()
	if !afterReconnect.OutboxPublisherReady || afterReconnect.OutboxReconnectSuccesses != 1 || afterReconnect.OutboxReconnectStatus != "success" {
		t.Fatalf("publisher state after reconnect = %+v", afterReconnect)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if metrics.Snapshot().OutboxLastPublishSuccess.IsZero() {
		t.Fatal("successful publish timestamp was not recorded")
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outbox after reconnect=%v err=%v", entries, err)
	}
}

func TestRelayKeepsPublisherUnreadyWhenReconnectVerificationFails(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	publisher := &reconnectPublisher{unavailable: true, reconnectErr: errors.New("replacement unavailable")}
	relay, err := NewRelay(store, publisher, RelayOptions{ReconnectAfterFailures: 1, ReconnectCooldown: time.Nanosecond, Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err == nil {
		t.Fatal("expected publish failure")
	}
	snapshot := metrics.Snapshot()
	if snapshot.OutboxPublisherReady || snapshot.OutboxReconnectFailures != 1 || snapshot.OutboxReconnectStatus != "failed" {
		t.Fatalf("publisher state after failed reconnect = %+v", snapshot)
	}
	if !snapshot.OutboxObserved || snapshot.OutboxPendingEntries != 1 {
		t.Fatalf("outbox state after first-entry failure = %+v, want one observable pending entry", snapshot)
	}
}

func TestRelayRecordsOutboxSnapshotAndDuplicateAcknowledgement(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	metrics, err := observability.NewViewMetrics(registry)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(store, &duplicatePublisher{testPublisher: &testPublisher{}}, RelayOptions{Metrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
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
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
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
	firstRelay, err := NewRelay(firstStore, publisher, RelayOptions{Metrics: firstMetrics, DeleteOutbox: func(ctx context.Context, ids []uint64) error {
		if firstDelete {
			firstDelete = false
			return deleteErr
		}
		return firstStore.DeleteOutbox(ctx, ids)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstRelay.flush(context.Background()); !errors.Is(err, deleteErr) {
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
	secondRelay, err := NewRelay(store, publisher, RelayOptions{Metrics: secondMetrics})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondRelay.flush(context.Background()); err != nil {
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
