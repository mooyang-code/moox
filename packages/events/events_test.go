package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBuiltInEvents(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cloudnode.job.execution.requested@1",
		"market.fetch.batch.completed@1",
		"observability.health.check.reported@1",
		"observability.host.snapshot.reported@1",
		"observability.metrics.snapshot.reported@1",
		"storage.dataset.factor_period.computed@1",
		"storage.dataset.period.collected@1",
		"storage.dataset.rows.upserted@2",
		"storage.dataset.sync_point@1",
		"storage.view.factor_period.ready@1",
		"storage.view.source_period.ready@1",
		"trade.target.requested@1",
	}
	wantOwners := map[string]string{
		"cloudnode.job.execution.requested@1":       "cloudnode",
		"observability.health.check.reported@1":     "watchdog",
		"observability.host.snapshot.reported@1":    "hostagent",
		"observability.metrics.snapshot.reported@1": "service",
		"market.fetch.batch.completed@1":            "collector",
		"storage.dataset.factor_period.computed@1":  "storage",
		"storage.dataset.period.collected@1":        "storage",
		"storage.dataset.rows.upserted@2":           "storage",
		"storage.dataset.sync_point@1":              "storage",
		"storage.view.factor_period.ready@1":        "storage",
		"storage.view.source_period.ready@1":        "storage",
		"trade.target.requested@1":                  "strategy",
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

func TestStorageCompletionEventsRoundTrip(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	now := timestamppb.Now()
	tests := []struct {
		name      string
		event     Event
		payload   proto.Message
		subjectID string
		decode    func([]byte, string, string) (proto.Message, error)
	}{
		{
			name: "dataset period collected", event: DatasetPeriodCollected,
			payload:   &storagepb.DatasetPeriodCollected{DatasetId: "dataset", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", SubjectIds: []string{"BTC-USDT"}, CollectedAt: now},
			subjectID: "dataset",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeDatasetPeriodCollected(registry, raw, subject, id)
				return payload, err
			},
		},
		{
			name: "source view ready", event: ViewSourcePeriodReady,
			payload:   &storagepb.ViewSourcePeriodReady{SourceViewId: "source-view", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Datasets: []*storagepb.ViewPeriodDatasetState{{DatasetId: "dataset", Status: "complete"}}, PrimarySubjects: []string{"BTC-USDT"}, ReadyAt: now},
			subjectID: "source-view",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeViewSourcePeriodReady(registry, raw, subject, id)
				return payload, err
			},
		},
		{
			name: "factor period computed", event: FactorPeriodComputed,
			payload:   &storagepb.FactorPeriodComputed{SourceViewId: "source-view", ResultDatasetId: "result-dataset", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Bindings: []*storagepb.FactorBindingPeriodState{{BindingId: "binding-1", FactorId: "factor-1", Status: "complete"}}, ComputedAt: now, TriggerEventId: "source-ready-1"},
			subjectID: "result-dataset",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeFactorPeriodComputed(registry, raw, subject, id)
				return payload, err
			},
		},
		{
			name: "factor view ready", event: ViewFactorPeriodReady,
			payload:   &storagepb.ViewFactorPeriodReady{SourceViewId: "source-view", ResultViewId: "result-view", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Bindings: []*storagepb.FactorBindingPeriodState{{BindingId: "binding-1", FactorId: "factor-1", Status: "complete"}}, ReadyAt: now},
			subjectID: "result-view",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeViewFactorPeriodReady(registry, raw, subject, id)
				return payload, err
			},
		},
		{
			name: "dataset sync point", event: DatasetSyncPoint,
			payload:   &storagepb.DatasetSyncPoint{SyncPointId: "sync-1", RequestId: "request-1", DatasetId: "dataset", Source: "import"},
			subjectID: "dataset",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeDatasetSyncPoint(registry, raw, subject, id)
				return payload, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := registry.Encode(tt.event, tt.payload, PublishOptions{
				EventID: "event-1", OccurredAt: now.AsTime(), SpaceID: "space", SubjectID: tt.subjectID,
			})
			require.NoError(t, err)
			raw, err := proto.Marshal(encoded.Message)
			require.NoError(t, err)
			decoded, err := tt.decode(raw, encoded.Subject, "event-1")
			require.NoError(t, err)
			require.True(t, proto.Equal(tt.payload, decoded), "decoded payload = %v", decoded)
		})
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
	_, err := registry.Encode(DatasetRowsUpserted, LogicalAccountTargetRequested.NewPayload(), PublishOptions{
		EventID: "event-1", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset",
	})
	if err == nil {
		t.Fatal("wrong payload was accepted")
	}
}

func TestLogicalAccountTargetRequestedContract(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	event, ok := registry.Lookup("trade.target.requested", 1)
	if !ok {
		t.Fatal("trade target event is not registered")
	}
	if event.Name() != LogicalAccountTargetRequested.Name() ||
		event.Stream() != "MOOX_TRADE" ||
		event.Owner() != "strategy" {
		t.Fatalf("unexpected trade target event: name=%q stream=%q owner=%q", event.Name(), event.Stream(), event.Owner())
	}
	if _, ok := event.NewPayload().(*tradeeventpb.LogicalAccountTargetRequested); !ok {
		t.Fatalf("payload type = %T, want *tradeeventpb.LogicalAccountTargetRequested", event.NewPayload())
	}
	subject, err := registry.RenderSubject(event, "space", "logical-1")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "moox.trade.target.requested.v1.onygcy3f.nrxwo2ldmfwc2mi" {
		t.Fatalf("subject = %q", subject)
	}
	if _, exists := registry.Lookup("trade.rebalance.requested", 1); exists {
		t.Fatal("legacy trade rebalance event remains registered")
	}
}
