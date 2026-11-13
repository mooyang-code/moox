package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// series_tag 的形状校验：补写事件的 series_tags 按同一规则检查。
func TestSeriesTagShapeValidation(t *testing.T) {
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
			start := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
			payload := &storagepb.CollectorKlineBackfilled{
				DatasetId: "dataset", Frequency: "1m", SubjectIds: []string{"BTC-USDT"}, Origin: "auto",
				Start: timestamppb.New(start), End: timestamppb.New(start.Add(time.Hour)), ConfirmedAt: timestamppb.New(start.Add(2 * time.Hour)),
			}
			if tt.tag != "" {
				payload.SeriesTags = []string{tt.tag}
			}
			_, err := registry.Encode(CollectorKlineBackfilled, payload, validationOptions("backfill-event-1", "space", "dataset"))
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
			name: "ledger route", event: DatasetPeriodCompleted, subjectID: "dataset",
			payload: validLedgerPeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetPeriodCompleted).DatasetId = "other" },
		},
		{
			name: "ledger failed subject subset", event: DatasetPeriodCompleted, subjectID: "dataset",
			payload: func() proto.Message {
				payload := validLedgerPeriodCompleted(now)
				payload.Status = "degraded"
				payload.FailedSubjects = []string{"BTC-USDT"}
				return payload
			}(),
			mutate: func(value proto.Message) {
				value.(*storagepb.DatasetPeriodCompleted).FailedSubjects = []string{"ETH-USDT"}
			},
		},
		{
			name: "ledger needs snapshot_ref", event: DatasetPeriodCompleted, subjectID: "dataset",
			payload: validLedgerPeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetPeriodCompleted).SnapshotRef = "" },
		},
		{
			name: "producer needs cause_event_id", event: DatasetPeriodCompleted, subjectID: "dataset_result",
			payload: validProducerPeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetPeriodCompleted).CauseEventId = "" },
		},
		{
			name: "unknown source", event: DatasetPeriodCompleted, subjectID: "dataset_result",
			payload: validProducerPeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetPeriodCompleted).Source = "collector" },
		},
		{
			name: "detail needs a type", event: DatasetPeriodCompleted, subjectID: "dataset_result",
			payload: validProducerPeriodCompleted(now),
			mutate:  func(value proto.Message) { value.(*storagepb.DatasetPeriodCompleted).DetailType = "" },
		},
		{
			name: "view data ready route", event: ViewDataReady, subjectID: "view-1",
			payload: validViewDataReady(now),
			mutate:  func(value proto.Message) { value.(*storagepb.ViewDataReady).ViewId = "other" },
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

func TestDatasetPeriodCompletedAllowsEmptySubjectIDs(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validLedgerPeriodCompleted(timestamppb.Now())
	payload.UniverseSubjectIds = nil
	payload.Frequency = "1H"
	payload.PeriodTime = 1
	_, err = registry.Encode(DatasetPeriodCompleted, payload, validationOptions("event-1", "space", "dataset"))
	require.NoError(t, err)
}

// 明细对 Storage 是不透明字节：内容怎么写都能通过，只要带类型。
func TestDatasetPeriodCompletedPassesOpaqueDetailThrough(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validProducerPeriodCompleted(timestamppb.Now())
	payload.Detail = []byte{0xff, 0x00, 0x01}
	encoded, err := registry.Encode(DatasetPeriodCompleted, payload, validationOptions("event-1", "space", "dataset_result"))
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	_, decoded, err := DecodeDatasetPeriodCompleted(registry, raw, encoded.Subject, "event-1")
	require.NoError(t, err)
	require.Equal(t, []byte{0xff, 0x00, 0x01}, decoded.GetDetail())
	require.Equal(t, "test.Detail.v1", decoded.GetDetailType())
}

type semanticValidationPublisher struct {
	calls int
}

func (p *semanticValidationPublisher) PublishRaw(context.Context, string, string, []byte, string) (*jetstream.PublishAck, error) {
	p.calls++
	return &jetstream.PublishAck{}, nil
}

func validationOptions(eventID, spaceID, subjectID string) PublishOptions {
	return PublishOptions{
		EventID: eventID, OccurredAt: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
		SpaceID: spaceID, SubjectID: subjectID,
	}
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

func TestDecodeAndPublishMessageRejectSemanticIdentityMismatch(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &hostmetricpb.HostMetric{AgentId: "aB3x", Hostname: "host-1", Snapshot: &hostmetricpb.HostSnapshot{}}
	encoded, err := registry.Encode(
		ObservabilityHostSnapshotReported,
		payload,
		validationOptions("host-event-1", "mooxsys", "aB3x"),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload.AgentId = "bC4y"
	encoded.Message.Payload, err = proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeRaw(registry, raw, encoded.Subject, "host-event-1", ContentType); err == nil {
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
