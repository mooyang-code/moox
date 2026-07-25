package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

func TestBuiltInEvents(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cloudnode.job.execution.requested@1",
		"metrics.host.reported@1",
		"metrics.snapshot.reported@1",
		"storage.dataset.rows.upserted@1",
		"trade.rebalance.requested@1",
	}
	events := registry.Events()
	if len(events) != len(want) {
		t.Fatalf("events = %d, want %d", len(events), len(want))
	}
	for i, event := range events {
		if got := eventKey(event); got != want[i] {
			t.Fatalf("event[%d] = %s, want %s", i, got, want[i])
		}
		if event.NewPayload() == nil || event.PayloadFullName() == "" || event.Stream() == "" || event.Owner() == "" {
			t.Fatalf("event %s is incomplete", want[i])
		}
		if family, err := registry.FamilyPattern(event); err != nil || family == "" {
			t.Fatalf("event %s family = %q, err=%v", want[i], family, err)
		}
	}
}

func TestRegistryEventsReturnsCopy(t *testing.T) {
	registry, _ := DefaultRegistry()
	first := registry.Events()
	first[0] = Event{}
	if registry.Events()[0].Name() == "" {
		t.Fatal("Events exposed registry storage")
	}
}

func TestDatasetRowsRoundTrip(t *testing.T) {
	registry, _ := DefaultRegistry()
	payload := &storagepb.DatasetRowsUpserted{
		SpaceId: "space", DatasetId: "dataset",
		Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{
			SpaceId: "space", DatasetId: "dataset",
			Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}},
		}}},
	}
	encoded, err := registry.Encode(DatasetRowsUpserted, payload, PublishOptions{
		EventID: "event-1", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	message, decoded, err := DecodeDatasetRowsUpserted(registry, raw, encoded.Subject, "event-1")
	if err != nil {
		t.Fatal(err)
	}
	if message.GetEventId() != "event-1" || decoded.GetDatasetId() != "dataset" {
		t.Fatalf("decoded = %v / %v", message, decoded)
	}
}

func TestEncodeRejectsWrongPayload(t *testing.T) {
	registry, _ := DefaultRegistry()
	_, err := registry.Encode(DatasetRowsUpserted, TradeRebalanceRequested.NewPayload(), PublishOptions{
		EventID: "event-1", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset",
	})
	if err == nil {
		t.Fatal("wrong payload was accepted")
	}
}
