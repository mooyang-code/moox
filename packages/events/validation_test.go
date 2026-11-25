package events

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// series_tag 的形状校验：完成事件里按序列的名单与行键同一套规则。
func TestSeriesTagShapeValidation(t *testing.T) {
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
			if err := validateSeriesTag(tt.tag); (err != nil) != tt.wantErr {
				t.Fatalf("validateSeriesTag() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStorageCompletionEventValidation(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	now := time.Now().UnixMilli()
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
			name: "complete has no failed subjects", event: DatasetPeriodCompleted, subjectID: "dataset",
			payload: validLedgerPeriodCompleted(now),
			mutate: func(value proto.Message) {
				value.(*storagepb.DatasetPeriodCompleted).FailedSubjects = []string{"BTC-USDT"}
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
			name: "view data ready route", event: ViewDataReady, subjectID: "dataset",
			payload: validViewDataReady(now),
			mutate:  func(value proto.Message) { value.(*storagepb.ViewDataReady).DatasetId = "other" },
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
	payload := validLedgerPeriodCompleted(time.Now().UnixMilli())
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
	payload := validProducerPeriodCompleted(time.Now().UnixMilli())
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
	payload := &metricspb.MetricReport{ServiceName: "storage", InstanceId: "storage-1", Snapshot: &metricspb.MetricSnapshot{}}
	encoded, err := registry.Encode(
		ObservabilityMetricsSnapshotReported,
		payload,
		validationOptions("metric-event-1", "mooxsys", "storage/storage-1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	payload.InstanceId = "other"
	encoded.Message.Payload, err = proto.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeRaw(registry, raw, encoded.Subject, "metric-event-1", ContentType); err == nil {
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

// 事件里的时刻都是 Unix 毫秒：0（没填）与负数（纪元之前）一律拒绝，信封与各事件体都一样。
func TestEventTimesRejectZeroAndNegative(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	now := time.Now().UnixMilli()
	for _, at := range []int64{0, -1} {
		cases := map[string]struct {
			event     Event
			subjectID string
			payload   proto.Message
		}{
			"period completed_at": {DatasetPeriodCompleted, "dataset", func() proto.Message { p := validLedgerPeriodCompleted(now); p.CompletedAt = at; return p }()},
			"view ready_at":       {ViewDataReady, "dataset", func() proto.Message { p := validViewDataReady(now); p.ReadyAt = at; return p }()},
			"backfilled confirmed_at": {CollectorKlineBackfilled, "dataset_src", &storagepb.CollectorKlineBackfilled{
				DatasetId: "dataset_src", Frequency: "5m", SubjectIds: []string{"BTC"}, Start: 1792576800, End: 1792580400, Origin: "auto", ConfirmedAt: at,
			}},
			"delist delist_at": {InstrumentDelistScheduled, InstrumentDelistSubjectID("binance", DelistMarketSpot, "FOOUSDT"), &storagepb.InstrumentDelistScheduled{
				Exchange: "binance", Market: DelistMarketSpot, ExchangeSymbol: "FOOUSDT", DelistAt: at, FirstSeenAt: now, Source: DelistSourceManual, Scope: DelistScopeAll,
			}},
			"delist first_seen_at": {InstrumentDelistScheduled, InstrumentDelistSubjectID("binance", DelistMarketSpot, "FOOUSDT"), &storagepb.InstrumentDelistScheduled{
				Exchange: "binance", Market: DelistMarketSpot, ExchangeSymbol: "FOOUSDT", DelistAt: now, FirstSeenAt: at, Source: DelistSourceManual, Scope: DelistScopeAll,
			}},
		}
		for name, c := range cases {
			_, err := registry.Encode(c.event, c.payload, validationOptions("event-1", "space", c.subjectID))
			require.Error(t, err, "%s = %d", name, at)
		}

		// 信封：编码时拒绝纪元及之前的时刻，解码与校验时拒绝 0 与负数。
		_, err := registry.Encode(DatasetPeriodCompleted, validLedgerPeriodCompleted(now), PublishOptions{
			EventID: "event-1", OccurredAt: time.UnixMilli(at), SpaceID: "space", SubjectID: "dataset",
		})
		require.Error(t, err, "encode occurred_at = %d", at)
		encoded, err := registry.Encode(DatasetPeriodCompleted, validLedgerPeriodCompleted(now), validationOptions("event-1", "space", "dataset"))
		require.NoError(t, err)
		encoded.Message.OccurredAt = at
		_, err = registry.ValidateMessage(encoded.Message)
		require.ErrorContains(t, err, "occurred_at", "validate occurred_at = %d", at)
		raw, err := proto.Marshal(encoded.Message)
		require.NoError(t, err)
		_, _, err = DecodeRaw(registry, raw, encoded.Subject, "event-1", ContentType)
		require.ErrorContains(t, err, "occurred_at", "decode occurred_at = %d", at)
	}
}
