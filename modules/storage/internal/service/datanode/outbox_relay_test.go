package datanode

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/messagepb"
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

func TestRelayStopsAtFailedEntryAndRetriesIt(t *testing.T) {
	store, err := pebble.Open(pebble.Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows := []*pb.RowFieldUpsert{{Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}, Fields: []*pb.FieldValue{{FieldId: "f", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}}}}
	_, err = store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetFieldsChangedMessage("node", spaceID, datasetID, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.WriteFieldsEvent(context.Background(), rows, func(spaceID, datasetID string, rows []*pb.RowFieldUpsert) ([]byte, error) {
		return pebble.BuildDatasetFieldsChangedMessage("node", spaceID, datasetID, rows)
	})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &testPublisher{failAt: 2}
	relay, err := NewOutboxRelay(store, publisher, OutboxRelayOptions{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Flush(context.Background()); err == nil {
		t.Fatal("expected failure")
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
	failedAttempt := &messagepb.MooxMessage{}
	retryAttempt := &messagepb.MooxMessage{}
	if err := proto.Unmarshal(publisher.attempts[1], failedAttempt); err != nil {
		t.Fatal(err)
	}
	if err := proto.Unmarshal(publisher.attempts[2], retryAttempt); err != nil {
		t.Fatal(err)
	}
	if failedAttempt.GetPublishedAt() == nil || retryAttempt.GetPublishedAt() == nil || !failedAttempt.GetPublishedAt().AsTime().Equal(retryAttempt.GetPublishedAt().AsTime()) {
		t.Fatalf("PublishedAt changed across retry: %v vs %v", failedAttempt.GetPublishedAt(), retryAttempt.GetPublishedAt())
	}
	if failedAttempt.GetMessageId() == "" || failedAttempt.GetMessageId() != retryAttempt.GetMessageId() {
		t.Fatalf("MessageID changed across retry: %q vs %q", failedAttempt.GetMessageId(), retryAttempt.GetMessageId())
	}
	entries, err = store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("remaining after retry=%v err=%v", entries, err)
	}
}
