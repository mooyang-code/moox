package events

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
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
		"moox.storage.dataset.rows.upserted.v2.space.dataset",
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
			payload := &storagepb.DatasetRowsUpserted{
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
			payload: &hostmetricpb.HostMetric{AgentId: "agent-1", Hostname: "host-1", Snapshot: &hostmetricpb.HostSnapshot{}},
			opts:    validationOptions("host-event-1", "moox_system", "agent-1"),
			mutate:  func(value proto.Message) { value.(*hostmetricpb.HostMetric).AgentId = "other" },
		},
		{
			name: "metrics producer", event: ObservabilityMetricsSnapshotReported,
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
			name:    "trade binding",
			event:   TradeTargetRequested,
			payload: validTargetIntent(),
			opts:    validationOptions("execution-1", "space", "binding-1"),
			mutate:  func(value proto.Message) { value.(*tradeeventpb.TargetIntent).ExecutionBindingId = "other" },
		},
		{
			name:    "trade event id",
			event:   TradeTargetRequested,
			payload: validTargetIntent(),
			opts:    validationOptions("execution-1", "space", "binding-1"),
			mutate:  func(value proto.Message) { value.(*tradeeventpb.TargetIntent).ExecutionId = "other" },
		},
		{
			name:    "trade command sequence",
			event:   TradeTargetRequested,
			payload: validTargetIntent(),
			opts:    validationOptions("execution-1", "space", "binding-1"),
			mutate:  func(value proto.Message) { value.(*tradeeventpb.TargetIntent).CommandSequence = 0 },
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
		CheckId:    "storage-health",
		Target:     "http://storage:8080/healthz",
		Kind:       "http",
		Success:    true,
		LatencyMs:  12,
		CheckedAt:  timestamppb.New(occurredAt),
	}
	opts := validationOptions("health-event-1", "moox_system", "storage-health")
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Encode(ObservabilityHealthCheckReported, valid, opts); err != nil {
		t.Fatalf("valid health check rejected: %v", err)
	}

	tests := map[string]func(*observabilitypb.HealthCheckReport){
		"observer required":   func(v *observabilitypb.HealthCheckReport) { v.ObserverId = "" },
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

func TestTradeTargetRequestedRejectsInvalidPayload(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tradeeventpb.TargetIntent)
	}{
		{name: "empty execution", mutate: func(value *tradeeventpb.TargetIntent) { value.ExecutionId = "" }},
		{name: "empty strategy run", mutate: func(value *tradeeventpb.TargetIntent) { value.StrategyRunId = "" }},
		{name: "empty binding", mutate: func(value *tradeeventpb.TargetIntent) { value.ExecutionBindingId = "" }},
		{name: "empty exchange account", mutate: func(value *tradeeventpb.TargetIntent) { value.ExchangeAccountId = "" }},
		{name: "empty data revision", mutate: func(value *tradeeventpb.TargetIntent) { value.DataRevision = "" }},
		{name: "zero command sequence", mutate: func(value *tradeeventpb.TargetIntent) { value.CommandSequence = 0 }},
		{name: "command sequence exceeds sqlite integer", mutate: func(value *tradeeventpb.TargetIntent) {
			value.CommandSequence = uint64(math.MaxInt64) + 1
		}},
		{name: "non-positive expiry", mutate: func(value *tradeeventpb.TargetIntent) { value.NotAfterUnixMs = 0 }},
		{name: "expired", mutate: func(value *tradeeventpb.TargetIntent) {
			value.NotAfterUnixMs = time.Now().Add(-time.Minute).UnixMilli()
		}},
		{name: "empty targets", mutate: func(value *tradeeventpb.TargetIntent) { value.Targets = nil }},
		{name: "duplicate symbol", mutate: func(value *tradeeventpb.TargetIntent) {
			value.Targets = append(value.Targets, &tradeeventpb.TargetPosition{
				InstrumentId: "BTC-USDT-SWAP", Symbol: value.Targets[0].GetSymbol(), TargetQuantity: "2",
			})
		}},
		{name: "blank instrument id", mutate: func(value *tradeeventpb.TargetIntent) {
			value.Targets[0].InstrumentId = " \t "
		}},
		{name: "empty symbol", mutate: func(value *tradeeventpb.TargetIntent) { value.Targets[0].Symbol = "" }},
		{name: "non-decimal target quantity", mutate: func(value *tradeeventpb.TargetIntent) {
			value.Targets[0].TargetQuantity = "one"
		}},
	}

	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := validTargetIntent()
			test.mutate(payload)
			if _, err := registry.Encode(
				TradeTargetRequested,
				payload,
				validationOptions("execution-1", "space", "binding-1"),
			); err == nil {
				t.Fatal("invalid target intent was accepted")
			}
		})
	}
}

func TestTradeTargetRequestedAcceptsCanonicalQuantitiesAndMaximumSequence(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, quantity := range []string{"0", "-0", "1", "-1", "1.25", "-0.0001"} {
		t.Run(quantity, func(t *testing.T) {
			payload := validTargetIntent()
			payload.CommandSequence = uint64(math.MaxInt64)
			payload.Targets[0].TargetQuantity = quantity
			if _, err := registry.Encode(
				TradeTargetRequested,
				payload,
				validationOptions("execution-1", "space", "binding-1"),
			); err != nil {
				t.Fatalf("canonical quantity %q rejected: %v", quantity, err)
			}
		})
	}
}

func TestTradeTargetRequestedRejectsNonCanonicalQuantities(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, quantity := range []string{
		"",
		"+1",
		".5",
		"1.",
		"01",
		"1e3",
		"1/2",
		"NaN",
		"Inf",
		" 1",
		strings.Repeat("9", 257),
	} {
		t.Run(quantity, func(t *testing.T) {
			payload := validTargetIntent()
			payload.Targets[0].TargetQuantity = quantity
			if _, err := registry.Encode(
				TradeTargetRequested,
				payload,
				validationOptions("execution-1", "space", "binding-1"),
			); err == nil {
				t.Fatalf("non-canonical quantity %q was accepted", quantity)
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

func validTargetIntent() *tradeeventpb.TargetIntent {
	return &tradeeventpb.TargetIntent{
		ExecutionId:        "execution-1",
		StrategyRunId:      "run-1",
		ExecutionBindingId: "binding-1",
		ExchangeAccountId:  "account-1",
		DataRevision:       "revision-1",
		CommandSequence:    1,
		NotAfterUnixMs:     time.Now().Add(time.Hour).UnixMilli(),
		Targets: []*tradeeventpb.TargetPosition{{
			InstrumentId:   "BTC-USDT",
			Symbol:         "BTCUSDT",
			TargetQuantity: "1.25",
		}},
	}
}
