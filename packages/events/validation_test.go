package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDatasetRowsUpsertedV1IsRejectedByV2Consumer(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got := DatasetRowsUpserted.Version(); got != 2 {
		t.Fatalf("storage event version = %d, want 2", got)
	}
	if _, ok := registry.Lookup(DatasetRowsUpserted.Name(), 1); ok {
		t.Fatal("v1 storage event remains registered")
	}
	encoded, err := registry.Encode(
		DatasetRowsUpserted,
		validRowsEvent(),
		validationOptions("storage-event-1", "space", "dataset"),
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded.Message.EventVersion = 1
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeDatasetRowsUpserted(
		registry,
		raw,
		"moox.event.storage.dataset.rows.upserted.v2.space.dataset",
		"storage-event-1",
	); err == nil {
		t.Fatal("v1 storage event was accepted by v2 consumer")
	}
}

func TestDatasetRowsUpsertedValidatesSeriesTagShape(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		{name: "default series"},
		{name: "opaque value", tag: "venue:okx"},
		{name: "too long", tag: strings.Repeat("x", 129), wantErr: true},
		{name: "leading whitespace", tag: " venue:okx", wantErr: true},
		{name: "trailing whitespace", tag: "venue:okx ", wantErr: true},
		{name: "control character", tag: "venue:\x00okx", wantErr: true},
		{name: "invalid utf8", tag: string([]byte{0xff}), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := &storagepb.DatasetRowsUpserted{SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1,
				SpaceId: "space", DatasetId: "dataset",
				Rows: []*storagepb.RowUpsert{{
					Key: &storagepb.RowKey{
						SpaceId: "space", DatasetId: "dataset",
						Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
							SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-29T00:00:00Z",
							SeriesTag: tt.tag,
						}},
					},
				}},
			}
			_, err := registry.Encode(
				DatasetRowsUpserted,
				payload,
				validationOptions("storage-event-1", "space", "dataset"),
			)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Encode() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStorageCompletionEventValidation(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	now := timestamppb.Now()
	tests := []struct {
		name      string
		event     Event
		payload   proto.Message
		subjectID string
		mutate    func(proto.Message)
	}{
		{
			name: "collector route", event: CollectorPeriodCompleted, subjectID: "dataset",
			payload: validCollectorPeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.CollectorPeriodCompleted).DatasetId = "other" },
		},
		{
			name: "collector failed subject subset", event: CollectorPeriodCompleted, subjectID: "dataset",
			payload: func() proto.Message {
				payload := validCollectorPeriodCompleted(now)
				payload.Status = "degraded"
				payload.FailedSubjects = []string{"BTC-USDT"}
				return payload
			}(),
			mutate: func(value proto.Message) {
				value.(*storagepb.CollectorPeriodCompleted).FailedSubjects = []string{"ETH-USDT"}
			},
		},
		{
			name: "merge incomplete position", event: MergePeriodCompleted, subjectID: "mdataset",
			payload: validMergePeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.MergePeriodCompleted).CommittedPositions[0].Sequence = 0 },
		},
		{
			name: "factor computed trigger", event: FactorPeriodComputed, subjectID: "mdataset",
			payload: validFactorPeriodComputed(now),
			mutate:  func(value proto.Message) { value.(*storagepb.FactorPeriodComputed).TriggerEventId = "" },
		},
		{
			name: "view data ready route", event: ViewDataReady, subjectID: "view-1",
			payload: validViewDataReady(now),
			mutate:  func(value proto.Message) { value.(*storagepb.ViewDataReady).ViewId = "other" },
		},
		{
			name: "sync point source", event: DatasetSyncPoint, subjectID: "dataset",
			payload: &storagepb.DatasetSyncPoint{SyncPointId: "sync-1", RequestId: "request-1", DatasetId: "dataset", Source: "import"},
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetSyncPoint).Source = "manual" },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid := proto.Clone(tt.payload)
			_, err := registry.Encode(tt.event, valid, validationOptions("event-1", "space", tt.subjectID))
			require.NoError(t, err)
			invalid := proto.Clone(tt.payload)
			tt.mutate(invalid)
			_, err = registry.Encode(tt.event, invalid, validationOptions("event-1", "space", tt.subjectID))
			require.Error(t, err)
		})
	}
}

func TestFactorCompletionRequiresSourceHash(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validFactorPeriodComputed(timestamppb.Now())
	payload.Bindings[0].SourceHash = ""
	_, err = registry.Encode(FactorPeriodComputed, payload, validationOptions("factor-event-1", "space", "mdataset"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "source_hash is required")
}

func TestCollectorPeriodCompletedAllowsEmptySubjectIDs(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validCollectorPeriodCompleted(timestamppb.Now())
	payload.ExpectedSubjectIds = nil
	payload.Frequency = "1H"
	payload.PeriodTime = 1
	_, err = registry.Encode(CollectorPeriodCompleted, payload, validationOptions("event-1", "space", "dataset"))
	require.NoError(t, err)
}

type semanticValidationPublisher struct {
	calls int
}

func (p *semanticValidationPublisher) PublishRaw(context.Context, string, string, []byte, string) (*jetstream.PublishAck, error) {
	p.calls++
	return &jetstream.PublishAck{}, nil
}

func TestEncodeRejectsEveryBuiltInEventIdentityMismatch(t *testing.T) {
	tests := []struct {
		name    string
		event   Event
		payload proto.Message
		opts    PublishOptions
		mutate  func(proto.Message)
	}{
		{
			name: "cloud job route", event: CloudJobExecutionRequested,
			payload: &cloudjobpb.JobExecutionRequested{JobId: "job-1", JobItemId: "item-1", JobType: "collect"},
			opts:    validationOptions("item-1", "space", "collect"),
			mutate:  func(value proto.Message) { value.(*cloudjobpb.JobExecutionRequested).JobType = "other" },
		},
		{
			name: "cloud job event id", event: CloudJobExecutionRequested,
			payload: &cloudjobpb.JobExecutionRequested{JobId: "job-1", JobItemId: "item-1", JobType: "collect"},
			opts:    validationOptions("item-1", "space", "collect"),
			mutate:  func(value proto.Message) { value.(*cloudjobpb.JobExecutionRequested).JobItemId = "other" },
		},
		{
			name: "host agent", event: ObservabilityHostSnapshotReported,
			payload: &hostmetricpb.HostMetric{AgentId: "aB3x", Hostname: "host-1", Snapshot: &hostmetricpb.HostSnapshot{}},
			opts:    validationOptions("host-event-1", "mooxsys", "aB3x"),
			mutate:  func(value proto.Message) { value.(*hostmetricpb.HostMetric).AgentId = "other" },
		},
		{
			name: "metrics producer", event: ObservabilityMetricsSnapshotReported,
			payload: &metricspb.MetricReport{ServiceName: "storage", InstanceId: "storage-1", Snapshot: &metricspb.MetricSnapshot{}},
			opts:    validationOptions("metric-event-1", "mooxsys", "storage/storage-1"),
			mutate:  func(value proto.Message) { value.(*metricspb.MetricReport).InstanceId = "other" },
		},
		{
			name: "storage dataset", event: DatasetRowsUpserted,
			payload: validRowsEvent(),
			opts:    validationOptions("storage-event-1", "space", "dataset"),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetRowsUpserted).DatasetId = "other" },
		},
		{
			name: "storage space", event: DatasetRowsUpserted,
			payload: validRowsEvent(),
			opts:    validationOptions("storage-event-1", "space", "dataset"),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetRowsUpserted).SpaceId = "other" },
		},
		{
			name: "storage row", event: DatasetRowsUpserted,
			payload: validRowsEvent(),
			opts:    validationOptions("storage-event-1", "space", "dataset"),
			mutate: func(value proto.Message) {
				value.(*storagepb.DatasetRowsUpserted).Rows[0].Key.DatasetId = "other"
			},
		},
	}
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := registry.Encode(test.event, test.payload, test.opts); err != nil {
				t.Fatalf("valid event rejected: %v", err)
			}
			invalid := proto.Clone(test.payload)
			test.mutate(invalid)
			if _, err := registry.Encode(test.event, invalid, test.opts); err == nil {
				t.Fatal("identity mismatch was accepted")
			}
		})
	}
}

func TestHealthCheckReportValidation(t *testing.T) {
	occurredAt := time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC)
	valid := &observabilitypb.HealthCheckReport{
		ObserverId: "scf-watchdog",
		NodeId:     "scf-node-a",
		CheckId:    "storage-health",
		Target:     "http://storage:8080/healthz",
		Kind:       "http",
		Success:    true,
		LatencyMs:  12,
		CheckedAt:  timestamppb.New(occurredAt),
	}
	opts := validationOptions("health-event-1", "mooxsys", "storage-health")
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Encode(ObservabilityHealthCheckReported, valid, opts); err != nil {
		t.Fatalf("valid health check rejected: %v", err)
	}

	tests := map[string]func(*observabilitypb.HealthCheckReport){
		"observer required":   func(v *observabilitypb.HealthCheckReport) { v.ObserverId = "" },
		"node too long":       func(v *observabilitypb.HealthCheckReport) { v.NodeId = string(make([]byte, 257)) },
		"check required":      func(v *observabilitypb.HealthCheckReport) { v.CheckId = "" },
		"kind required":       func(v *observabilitypb.HealthCheckReport) { v.Kind = "" },
		"target too long":     func(v *observabilitypb.HealthCheckReport) { v.Target = string(make([]byte, 513)) },
		"error code too long": func(v *observabilitypb.HealthCheckReport) { v.ErrorCode = string(make([]byte, 65)) },
		"error summary too long": func(v *observabilitypb.HealthCheckReport) {
			v.ErrorSummary = string(make([]byte, 257))
		},
		"negative latency":   func(v *observabilitypb.HealthCheckReport) { v.LatencyMs = -1 },
		"checked at missing": func(v *observabilitypb.HealthCheckReport) { v.CheckedAt = nil },
		"checked at invalid": func(v *observabilitypb.HealthCheckReport) {
			v.CheckedAt = &timestamppb.Timestamp{Seconds: 253402300800}
		},
		"checked at too early": func(v *observabilitypb.HealthCheckReport) {
			v.CheckedAt = timestamppb.New(occurredAt.Add(-5*time.Minute - time.Nanosecond))
		},
		"checked at too late": func(v *observabilitypb.HealthCheckReport) {
			v.CheckedAt = timestamppb.New(occurredAt.Add(5*time.Minute + time.Nanosecond))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			invalid := proto.Clone(valid).(*observabilitypb.HealthCheckReport)
			mutate(invalid)
			if _, err := registry.Encode(ObservabilityHealthCheckReported, invalid, opts); err == nil {
				t.Fatal("invalid health check was accepted")
			}
		})
	}
}

func TestDecodeAndPublishMessageRejectSemanticIdentityMismatch(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &cloudjobpb.JobExecutionRequested{
		JobId: "job-1", JobItemId: "item-1", JobType: "collect",
	}
	encoded, err := registry.Encode(
		CloudJobExecutionRequested,
		payload,
		validationOptions("item-1", "space", "collect"),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload.JobType = "other"
	encoded.Message.Payload, err = proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeRaw(registry, raw, encoded.Subject, "item-1", ContentType); err == nil {
		t.Fatal("DecodeRaw accepted mismatched payload identity")
	}
	if _, err := registry.SubjectForMessage(encoded.Message); err == nil {
		t.Fatal("SubjectForMessage accepted mismatched payload identity")
	}
	rawPublisher := &semanticValidationPublisher{}
	publisher, err := NewPublisher(rawPublisher, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishMessage(context.Background(), encoded.Message); err == nil {
		t.Fatal("PublishMessage accepted mismatched payload identity")
	}
	if rawPublisher.calls != 0 {
		t.Fatalf("invalid message reached raw publisher %d times", rawPublisher.calls)
	}
}

func validationOptions(eventID, spaceID, subjectID string) PublishOptions {
	return PublishOptions{
		EventID: eventID, OccurredAt: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
		SpaceID: spaceID, SubjectID: subjectID,
	}
}

func validRowsEvent() *storagepb.DatasetRowsUpserted {
	return &storagepb.DatasetRowsUpserted{SourceNodeId: "node", SourceStoreId: "store", SourceSequence: 1,
		SpaceId: "space", DatasetId: "dataset",
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{
				SpaceId: "space", DatasetId: "dataset",
				Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}},
			},
		}},
	}
}
