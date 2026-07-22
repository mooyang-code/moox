package datanode

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type testPublisher struct {
	values [][]byte
	failAt int
}

func (p *testPublisher) PublishMessage(_ context.Context, data []byte) error {
	if p.failAt > 0 && len(p.values)+1 == p.failAt {
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
	entries, err = store.ListOutbox(context.Background(), 0, 10)
	if err != nil || len(entries) != 0 {
		t.Fatalf("remaining after retry=%v err=%v", entries, err)
	}
}
