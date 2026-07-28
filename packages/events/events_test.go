package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestBuiltInEvents(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cloudnode.job.execution.requested@1",
		"observability.health.check.reported@1",
		"observability.host.snapshot.reported@1",
		"observability.metrics.snapshot.reported@1",
		"storage.dataset.rows.upserted@1",
		"trade.rebalance.requested@1",
	}
	wantOwners := map[string]string{
		"cloudnode.job.execution.requested@1":       "cloudnode",
		"observability.health.check.reported@1":     "watchdog",
		"observability.host.snapshot.reported@1":    "hostagent",
		"observability.metrics.snapshot.reported@1": "service",
		"storage.dataset.rows.upserted@1":           "storage",
		"trade.rebalance.requested@1":               "strategy",
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
		if event.Owner() != wantOwners[want[i]] {
			t.Fatalf("event %s owner = %q, want %q", want[i], event.Owner(), wantOwners[want[i]])
		}
		if family, err := registry.FamilyPattern(event); err != nil || family == "" {
			t.Fatalf("event %s family = %q, err=%v", want[i], family, err)
		}
	}
}

func TestObservabilityEventsShareOneStream(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	for _, event := range []Event{
		ObservabilityMetricsSnapshotReported,
		ObservabilityHostSnapshotReported,
		ObservabilityHealthCheckReported,
	} {
		require.Equal(t, "MOOX_OBSERVABILITY", event.Stream())
		family, err := registry.FamilyPattern(event)
		require.NoError(t, err)
		require.Contains(t, family, "moox.observability.")
	}
	for _, event := range registry.Events() {
		require.NotEqual(t, "MOOX_METRICS", event.Stream())
		require.NotEqual(t, "metrics.host.reported", event.Name())
		require.NotEqual(t, "metrics.snapshot.reported", event.Name())
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
