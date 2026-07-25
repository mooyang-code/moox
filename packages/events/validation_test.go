package events

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
)

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
			payload: &cloudjobpb.JobExecutionRequested{JobId: "job-1", JobItemId: "item-1", CodePackageId: "package-1", JobType: "collect"},
			opts:    validationOptions("item-1", "space", "package-1/collect"),
			mutate:  func(value proto.Message) { value.(*cloudjobpb.JobExecutionRequested).CodePackageId = "other" },
		},
		{
			name: "cloud job event id", event: CloudJobExecutionRequested,
			payload: &cloudjobpb.JobExecutionRequested{JobId: "job-1", JobItemId: "item-1", CodePackageId: "package-1", JobType: "collect"},
			opts:    validationOptions("item-1", "space", "package-1/collect"),
			mutate:  func(value proto.Message) { value.(*cloudjobpb.JobExecutionRequested).JobItemId = "other" },
		},
		{
			name: "host agent", event: MetricsHostReported,
			payload: &hostmetricpb.HostMetric{AgentId: "agent-1", Hostname: "host-1", Snapshot: &hostmetricpb.HostSnapshot{}},
			opts:    validationOptions("host-event-1", "moox_system", "agent-1"),
			mutate:  func(value proto.Message) { value.(*hostmetricpb.HostMetric).AgentId = "other" },
		},
		{
			name: "metrics producer", event: MetricsSnapshotReported,
			payload: &metricspb.MetricReport{ServiceName: "storage", InstanceId: "storage-1", Snapshot: &metricspb.MetricSnapshot{}},
			opts:    validationOptions("metric-event-1", "moox_system", "storage/storage-1"),
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
		{
			name: "trade binding", event: TradeRebalanceRequested,
			payload: &tradeeventpb.RebalanceRequested{
				RequestId: "trade-event-1", StrategyRunId: "run-1", ExecutionBindingId: "execution-1",
				AccountId: "account-1", ChannelId: "channel-1", Mode: "paper", DataRevision: "revision-1",
				CapitalAmount: "100", QuoteAsset: "USDT", CommandSequence: 1,
			},
			opts:   validationOptions("trade-event-1", "space", "execution-1"),
			mutate: func(value proto.Message) { value.(*tradeeventpb.RebalanceRequested).ExecutionBindingId = "other" },
		},
		{
			name: "trade event id", event: TradeRebalanceRequested,
			payload: &tradeeventpb.RebalanceRequested{
				RequestId: "trade-event-1", StrategyRunId: "run-1", ExecutionBindingId: "execution-1",
				AccountId: "account-1", ChannelId: "channel-1", Mode: "paper", DataRevision: "revision-1",
				CapitalAmount: "100", QuoteAsset: "USDT", CommandSequence: 1,
			},
			opts:   validationOptions("trade-event-1", "space", "execution-1"),
			mutate: func(value proto.Message) { value.(*tradeeventpb.RebalanceRequested).RequestId = "other" },
		},
		{
			name: "trade command sequence", event: TradeRebalanceRequested,
			payload: &tradeeventpb.RebalanceRequested{
				RequestId: "trade-event-1", StrategyRunId: "run-1", ExecutionBindingId: "execution-1",
				AccountId: "account-1", ChannelId: "channel-1", Mode: "paper", DataRevision: "revision-1",
				CapitalAmount: "100", QuoteAsset: "USDT", CommandSequence: 1,
			},
			opts:   validationOptions("trade-event-1", "space", "execution-1"),
			mutate: func(value proto.Message) { value.(*tradeeventpb.RebalanceRequested).CommandSequence = 0 },
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
	payload := &cloudjobpb.JobExecutionRequested{
		JobId: "job-1", JobItemId: "item-1", CodePackageId: "package-1", JobType: "collect",
	}
	encoded, err := registry.Encode(
		CloudJobExecutionRequested,
		payload,
		validationOptions("item-1", "space", "package-1/collect"),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload.CodePackageId = "other"
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
	return &storagepb.DatasetRowsUpserted{
		SpaceId: "space", DatasetId: "dataset",
		Rows: []*storagepb.RowUpsert{{
			Key: &storagepb.RowKey{
				SpaceId: "space", DatasetId: "dataset",
				Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}},
			},
		}},
	}
}
