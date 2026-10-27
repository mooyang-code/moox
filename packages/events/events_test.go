package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
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
		"event.collector.kline.backfilled@1",
		"event.observability.host.snapshot.reported@1",
		"event.observability.metrics.snapshot.reported@1",
		"event.storage.dataset.period.completed@1",
		"event.storage.dataset.rows.upserted@2",
		"event.storage.dataset.sync_point@1",
		"event.storage.view.data.ready@2",
		"event.trade.target.weight_requested@1",
	}
	wantOwners := map[string]string{
		"event.collector.kline.backfilled@1":              "collector",
		"event.observability.host.snapshot.reported@1":    "hostagent",
		"event.observability.metrics.snapshot.reported@1": "service",
		"event.storage.dataset.period.completed@1":        "storage",
		"event.storage.dataset.rows.upserted@2":           "storage",
		"event.storage.dataset.sync_point@1":              "storage",
		"event.storage.view.data.ready@2":                 "storage",
		"event.trade.target.weight_requested@1":           "strategy",
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

func TestRegistryRejectsEventWithoutPrefix(t *testing.T) {
	_, err := newRegistry([]Event{{
		name:       "storage.dataset.rows.upserted",
		version:    1,
		stream:     "MOOX_STORAGE",
		owner:      "storage",
		newPayload: func() proto.Message { return &storagepb.DatasetSyncPoint{} },
		validate:   func(*eventpb.EventMessage, proto.Message) error { return nil },
	}})
	require.EqualError(t, err, "event name \"storage.dataset.rows.upserted\" must start with event.")
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
			name: "dataset period completed (ledger)", event: DatasetPeriodCompleted,
			payload:   validLedgerPeriodCompleted(now),
			subjectID: "dataset",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeDatasetPeriodCompleted(registry, raw, subject, id)
				return payload, err
			},
		},
		{
			name: "view data ready", event: ViewDataReady,
			payload:   validViewDataReady(now),
			subjectID: "view-1",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeViewDataReady(registry, raw, subject, id)
				return payload, err
			},
		},
		{
			name: "dataset period completed (producer)", event: DatasetPeriodCompleted,
			payload:   validProducerPeriodCompleted(now),
			subjectID: "dataset_result",
			decode: func(raw []byte, subject, id string) (proto.Message, error) {
				_, payload, err := DecodeDatasetPeriodCompleted(registry, raw, subject, id)
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
	} {
		require.Equal(t, "MOOX_OBSERVABILITY", event.Stream())
		family, err := registry.FamilyPattern(event)
		require.NoError(t, err)
		require.Contains(t, family, "moox.event.observability.")
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
	payload := &storagepb.DatasetRowsUpserted{NodeId: "node", StoreId: "store", Sequence: 1,
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
	_, err := registry.Encode(DatasetRowsUpserted, LogicalAccountTargetWeightRequested.NewPayload(), PublishOptions{
		EventID: "event-1", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset",
	})
	if err == nil {
		t.Fatal("wrong payload was accepted")
	}
}

func TestLogicalAccountTargetWeightRequestedContract(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	event, ok := registry.Lookup("event.trade.target.weight_requested", 1)
	if !ok {
		t.Fatal("trade target event is not registered")
	}
	if event.Name() != LogicalAccountTargetWeightRequested.Name() ||
		event.Stream() != "MOOX_TRADE" ||
		event.Owner() != "strategy" {
		t.Fatalf("unexpected trade target event: name=%q stream=%q owner=%q", event.Name(), event.Stream(), event.Owner())
	}
	if _, ok := event.NewPayload().(*tradeeventpb.LogicalAccountTargetWeightRequested); !ok {
		t.Fatalf("payload type = %T, want *tradeeventpb.LogicalAccountTargetWeightRequested", event.NewPayload())
	}
	subject, err := registry.RenderSubject(event, "space", "logical-1")
	if err != nil {
		t.Fatal(err)
	}
	if subject != "moox.event.trade.target.weight_requested.v1.onygcy3f.nrxwo2ldmfwc2mi" {
		t.Fatalf("subject = %q", subject)
	}
	if _, exists := registry.Lookup("event.trade.target.requested", 1); exists {
		t.Fatal("legacy trade target event remains registered")
	}
	if _, exists := registry.Lookup("trade.rebalance.requested", 1); exists {
		t.Fatal("legacy trade rebalance event remains registered")
	}
}

func validLedgerPeriodCompleted(now *timestamppb.Timestamp) *storagepb.DatasetPeriodCompleted {
	return &storagepb.DatasetPeriodCompleted{
		DatasetId: "dataset", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Source: "ledger",
		SnapshotRef:        "series-hash-1",
		UniverseSubjectIds: []string{"BTC-USDT"},
		CommittedPositions: []*storagepb.CommittedPosition{{NodeId: "node", StoreId: "store", Sequence: 1}},
		CompletedAt:        now,
	}
}

func validViewDataReady(now *timestamppb.Timestamp) *storagepb.ViewDataReady {
	return &storagepb.ViewDataReady{
		ViewId: "view-1", ViewConfigId: "view-config-1", CompletionEventId: "period-completed-1",
		DatasetId: "dataset", Status: "complete", VisibleScope: "universe:dataset:1m",
		Frequency: "1m", PeriodTime: 1786032000,
		CommittedPositions: []*storagepb.CommittedPosition{{NodeId: "node", StoreId: "store", Sequence: 2}},
		ReadyAt:            now,
	}
}

// validProducerPeriodCompleted 是写入方自己报告的完成：明细是不透明字节，Storage 只要求带类型。
func validProducerPeriodCompleted(now *timestamppb.Timestamp) *storagepb.DatasetPeriodCompleted {
	return &storagepb.DatasetPeriodCompleted{
		DatasetId: "dataset_result", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Source: "producer",
		UniverseSubjectIds: []string{"BTC-USDT"}, CauseEventId: "period-completed-1", CompletedAt: now,
		DetailType: "test.Detail.v1", Detail: []byte("opaque"),
	}
}

func TestCollectorKlineBackfilledRoundTripAndValidation(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	start := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	payload := &storagepb.CollectorKlineBackfilled{
		DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC", "ETH"},
		Start: timestamppb.New(start), End: timestamppb.New(start.Add(time.Hour)),
		Origin: "auto", JobId: "job-1", SyncRequestId: "batch-1", ConfirmedAt: timestamppb.New(start.Add(2 * time.Hour)),
	}
	raw, err := registry.MarshalMessage(CollectorKlineBackfilled, payload, PublishOptions{EventID: "batch-1", OccurredAt: start, SpaceID: "crypto", SubjectID: "dataset_src"})
	require.NoError(t, err)
	message, err := registry.UnmarshalMessage(raw)
	require.NoError(t, err)
	subject, err := registry.SubjectForMessage(message)
	require.NoError(t, err)
	require.Contains(t, subject, "moox.event.collector.kline.backfilled.v1.")
	require.Equal(t, "MOOX_COLLECTOR", CollectorKlineBackfilled.Stream())

	for name, mutate := range map[string]func(*storagepb.CollectorKlineBackfilled){
		"dataset mismatch": func(p *storagepb.CollectorKlineBackfilled) { p.DatasetId = "other" },
		"no subjects":      func(p *storagepb.CollectorKlineBackfilled) { p.SubjectIds = nil },
		"empty range":      func(p *storagepb.CollectorKlineBackfilled) { p.End = p.Start },
		"bad origin":       func(p *storagepb.CollectorKlineBackfilled) { p.Origin = "other" },
		"no sync request":  func(p *storagepb.CollectorKlineBackfilled) { p.SyncRequestId = "" },
	} {
		broken := proto.Clone(payload).(*storagepb.CollectorKlineBackfilled)
		mutate(broken)
		_, err := registry.Encode(CollectorKlineBackfilled, broken, PublishOptions{EventID: "batch-1", OccurredAt: start, SpaceID: "crypto", SubjectID: "dataset_src"})
		require.Error(t, err, name)
	}
}
