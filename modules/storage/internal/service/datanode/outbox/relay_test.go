package outbox

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
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

type gatedPublisher struct {
	entered     chan struct{}
	release     chan struct{}
	published   chan struct{}
	enteredOnce sync.Once
	calls       atomic.Int32
}

func (p *gatedPublisher) PublishMessage(ctx context.Context, _ []byte) error {
	p.calls.Add(1)
	p.enteredOnce.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		close(p.published)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

func TestRelayDropsUnsupportedLegacyOutboxEventsAndPublishesTheRest(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	legacy, err := proto.Marshal(&eventpb.EventMessage{
		EventId: "legacy-period-collected", EventName: "event.storage.dataset.period.collected", EventVersion: 1,
		SpaceId: "crypto", SubjectId: "dataset_binance_kline_1m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertOutboxPayloadForTest(legacy); err != nil {
		t.Fatal(err)
	}
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.values) != 1 {
		t.Fatalf("published %d events, want 1 valid rows.upserted", len(publisher.values))
	}
	published := &eventpb.EventMessage{}
	if err := proto.Unmarshal(publisher.values[0], published); err != nil {
		t.Fatal(err)
	}
	if published.GetEventName() != "event.storage.dataset.rows.upserted" {
		t.Fatalf("published event %q, want rows.upserted", published.GetEventName())
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("remaining outbox=%v err=%v", entries, err)
	}
}

func TestRelayPublicationIsSerializedWithDatasetPurge(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insertRecordOutboxEvent(t, store, "space", "dataset-purge")
	publisher := &gatedPublisher{entered: make(chan struct{}), release: make(chan struct{}), published: make(chan struct{})}
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10, PublishTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	flushDone := make(chan error, 1)
	go func() { flushDone <- relay.flush(context.Background()) }()
	select {
	case <-publisher.entered:
	case <-time.After(time.Second):
		t.Fatal("relay did not enter publisher")
	}

	otherDatasetWrite := make(chan error, 1)
	go func() {
		row := &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset-other", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
		}
		otherDatasetWrite <- store.UpsertFields(context.Background(), []*pb.RowFieldUpsert{row})
	}()
	select {
	case err := <-otherDatasetWrite:
		if err != nil {
			t.Fatalf("write to another dataset during publish: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow publication blocked a write to another dataset")
	}

	deleteStarted := make(chan struct{})
	deleteDone := make(chan error, 1)
	go func() {
		close(deleteStarted)
		_, deleteErr := store.DeleteDatasetRows(context.Background(), "space", "dataset-purge")
		deleteDone <- deleteErr
	}()
	<-deleteStarted
	select {
	case err := <-deleteDone:
		t.Fatalf("purge completed while an outbox publication held its lease: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(publisher.release)
	if err := <-flushDone; err != nil {
		t.Fatalf("relay flush: %v", err)
	}
	select {
	case <-publisher.published:
	case <-time.After(time.Second):
		t.Fatal("publisher did not confirm the event")
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("dataset purge: %v", err)
	}

	if err := relay.flush(context.Background()); err != nil {
		t.Fatalf("relay flush after purge: %v", err)
	}
	if calls := publisher.calls.Load(); calls != 1 {
		t.Fatalf("publisher calls = %d after purge, want 1", calls)
	}
}

func TestPurgeWinningOutboxLockPreventsPublication(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insertRecordOutboxEvent(t, store, "space", "dataset-purge")
	if _, err := store.DeleteDatasetRows(context.Background(), "space", "dataset-purge"); err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.values) != 0 {
		t.Fatalf("published %d events after purge won the outbox lock", len(publisher.values))
	}
}

func insertRecordOutboxEvent(t *testing.T, store *pebble.Store, spaceID, datasetID string) {
	t.Helper()
	rows := []*pb.RowFieldUpsert{{
		Key:    &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "value"}}}},
	}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
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

// 无法通过当前契约校验的记录（例如旧版本写入、缺少 definition_hash 的因子周期标记）永远发不出去：
// relay 隔离它并继续投递其后的事件，不能堵住整个 DataNode 的出站队列。
func TestRelayQuarantinesInvalidOutboxEventsAndPublishesTheRest(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := proto.Marshal(&storageeventpb.FactorPeriodComputed{
		DatasetId: "result", SourceDatasetId: "source", Frequency: "1m", PeriodTime: 1786032000, Status: "complete",
		UniverseSubjectIds: []string{"BTC"}, Factors: []*storageeventpb.FactorPeriodState{{FactorId: "momentum", Status: "complete", SourceHash: "hash-1"}},
		TriggerEventId: "collector-event", ComputedAt: timestamppb.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := registry.Lookup(events.FactorPeriodComputed.Name(), events.FactorPeriodComputed.Version())
	if !ok {
		t.Fatal("因子周期事件未注册")
	}
	legacy, err := proto.Marshal(&eventpb.EventMessage{
		EventId: "legacy-factor-period", EventName: registered.Name(), EventVersion: registered.Version(),
		SpaceId: "crypto", SubjectId: "result", OccurredAt: timestamppb.Now(), Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertOutboxPayloadForTest(legacy); err != nil {
		t.Fatal(err)
	}
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	if _, err := store.UpsertFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetRowsUpsertedMessage("node", spaceID, datasetID, rows)
	}); err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{}
	relay, err := NewRelay(store, publisher, RelayOptions{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(publisher.values) != 1 {
		t.Fatalf("应发布其后的 1 条有效事件，实际 %d 条", len(publisher.values))
	}
	entries, err := store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("坏记录应被隔离删除：remaining=%v err=%v", entries, err)
	}
}
