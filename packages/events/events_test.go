package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/events/tradingpb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDefaultRegistryHasExplicitEvents(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []EventType{TradingSignal, DatasetRowsUpserted, TradeOrderIntentCreated, TradeOrderStateChanged, TradeExecutionSliceReady} {
		if _, ok := r.Schema(event); !ok {
			t.Fatalf("event %s is not registered", eventKey(event))
		}
	}
}

func TestRegisteredPayloadDescriptorsHaveCanonicalFullNames(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []protoreflect.FullName{
		"trpc.moox.trading.TradingSignal",
		"trpc.moox.storage.event.DatasetRowsUpserted",
	} {
		factory, ok := r.PayloadFactory(name)
		if !ok {
			t.Fatalf("payload factory missing for %q", name)
		}
		if got := factory().ProtoReflect().Descriptor().FullName(); got != name {
			t.Fatalf("payload descriptor = %q, want %q", got, name)
		}
		if _, err := protoregistry.GlobalTypes.FindMessageByName(name); err != nil {
			t.Fatalf("payload descriptor %q is not globally registered: %v", name, err)
		}
	}
}

func TestSubjectFamilyPatternIsDerivedFromValidatedTemplate(t *testing.T) {
	template, err := NewSubjectTemplate("moox.market.kline.v1.<space>.<subject>.detail")
	if err != nil {
		t.Fatal(err)
	}
	if got := template.FamilyPattern(); got != "moox.market.kline.v1.>" {
		t.Fatalf("family pattern = %q", got)
	}
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.FamilyPattern(DatasetRowsUpserted); err != nil || got != "moox.storage.dataset.rows.upserted.v1.>" {
		t.Fatalf("registry family pattern = %q, err=%v", got, err)
	}
}

func TestRegistryRejectsUnknownPayload(t *testing.T) {
	_, err := NewRegistry([]byte("version: 1\nevents:\n  - name: x.y\n    version: 1\n    payload: unknown.Payload\n    subject: moox.x.y.v1.<space>.<subject>\n    stream: MOOX_TEST\n    partition_key: subject_id\n    owner: test\n"))
	if err == nil {
		t.Fatal("NewRegistry() error = nil, want unknown payload error")
	}
}

func TestDecodeDatasetRowsUpsertedUsesSharedContract(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := r.Encode(DatasetRowsUpserted, &storagepb.DatasetRowsUpserted{SpaceId: "space", DatasetId: "dataset", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "record-1", Version: "v1"}}}}}}, PublishOptions{
		EventID: "storage-event-1", OccurredAt: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), SpaceID: "space", SubjectID: "dataset",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	message, payload, err := DecodeDatasetRowsUpserted(r, raw, encoded.Subject, "storage-event-1")
	if err != nil {
		t.Fatal(err)
	}
	if message.GetEventId() != "storage-event-1" || payload.GetSpaceId() != "space" || payload.GetDatasetId() != "dataset" || len(payload.GetRows()) != 1 {
		t.Fatalf("decoded storage event = %v / %v", message, payload)
	}
	if _, _, err := DecodeDatasetRowsUpserted(r, raw, encoded.Subject, "wrong-id"); err == nil {
		t.Fatal("DecodeDatasetRowsUpserted() error = nil for mismatched NATS message id")
	}
}

func TestDecodeDatasetRowsUpsertedRejectsRowIdentityMismatch(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := r.Encode(DatasetRowsUpserted, &storagepb.DatasetRowsUpserted{SpaceId: "space", DatasetId: "dataset", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "other", DatasetId: "dataset"}}}}, PublishOptions{
		EventID: "storage-event-identity", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeDatasetRowsUpserted(r, raw, encoded.Subject, "storage-event-identity"); err == nil {
		t.Fatal("row identity mismatch was accepted")
	}
}

func TestDecodeDatasetRowsUpsertedRejectsMalformedStructuredRow(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := r.Encode(DatasetRowsUpserted, &storagepb.DatasetRowsUpserted{
		SpaceId: "space", DatasetId: "dataset",
		Rows: []*storagepb.RowUpsert{{
			Key:    &storagepb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "not-a-time"}}},
			Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{}}},
		}},
	}, PublishOptions{EventID: "storage-malformed", OccurredAt: time.Now().UTC(), SpaceID: "space", SubjectID: "dataset"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeDatasetRowsUpserted(r, raw, encoded.Subject, encoded.Message.GetEventId()); err == nil {
		t.Fatal("DecodeDatasetRowsUpserted() accepted malformed time-series/value data")
	}
}

func TestTradingSignalPayloadContract(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &tradingpb.TradingSignal{StrategyId: "mean-reversion", SignalId: "signal-1", Symbol: "BTC-USDT", Side: tradingpb.SignalSide_SIGNAL_SIDE_BUY, Action: tradingpb.SignalAction_SIGNAL_ACTION_OPEN, SignalTime: timestamppb.Now()}
	if err := ValidateTradingSignal(payload); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Encode(TradingSignal, payload, PublishOptions{EventID: "signal-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "BTC-USDT"}); err != nil {
		t.Fatal(err)
	}
	if payload.TargetPrice != nil || payload.StopLossPrice != nil || payload.TakeProfitPrice != nil {
		t.Fatal("optional prices should remain absent")
	}
	target, stop, take := 101.5, 98.5, 105.0
	withPrices := proto.Clone(payload).(*tradingpb.TradingSignal)
	withPrices.TargetPrice, withPrices.StopLossPrice, withPrices.TakeProfitPrice = &target, &stop, &take
	encoded, err := r.Encode(TradingSignal, withPrices, PublishOptions{EventID: "signal-2", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "BTC-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	factory, ok := r.PayloadFactory("trpc.moox.trading.TradingSignal")
	if !ok {
		t.Fatal("trading signal payload factory is missing")
	}
	decoded := factory().(*tradingpb.TradingSignal)
	if err := proto.Unmarshal(encoded.Message.GetPayload(), decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetTargetPrice() != target || decoded.GetStopLossPrice() != stop || decoded.GetTakeProfitPrice() != take {
		t.Fatalf("optional prices were not preserved: %v", decoded)
	}
}

func TestDecodeTradingSignalValidatesSubjectIdentity(t *testing.T) {
	r, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &tradingpb.TradingSignal{
		StrategyId: "strategy-1", SignalId: "signal-1", Symbol: "BTC-USDT",
		Side: tradingpb.SignalSide_SIGNAL_SIDE_BUY, Action: tradingpb.SignalAction_SIGNAL_ACTION_OPEN,
		SignalTime: timestamppb.New(time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)),
	}
	encoded, err := r.Encode(TradingSignal, payload, PublishOptions{
		EventID: "signal-event-1", OccurredAt: time.Date(2026, 7, 23, 10, 0, 1, 0, time.UTC), SpaceID: "space", SubjectID: "BTC-USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := proto.Marshal(encoded.Message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeTradingSignal(r, raw, encoded.Subject, encoded.Message.GetEventId()); err != nil {
		t.Fatalf("DecodeTradingSignal() error = %v", err)
	}
	message := proto.Clone(encoded.Message).(*eventpb.EventMessage)
	message.SubjectId = "ETH-USDT"
	raw, err = proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DecodeTradingSignal(r, raw, encoded.Subject, encoded.Message.GetEventId()); err == nil {
		t.Fatal("DecodeTradingSignal() accepted a subject identity mismatch")
	}
	if _, _, err := DecodeTradingSignalWithContentType(r, raw, encoded.Subject, encoded.Message.GetEventId(), "application/x-protobuf"); err == nil {
		t.Fatal("DecodeTradingSignalWithContentType() accepted an unexpected content type")
	}
}

func TestTradingSignalUsesOpenCloseActionsWithoutStrength(t *testing.T) {
	actions := tradingpb.SignalAction(0).Descriptor().Values()
	for _, name := range []string{"SIGNAL_ACTION_OPEN", "SIGNAL_ACTION_CLOSE", "SIGNAL_ACTION_INCREASE", "SIGNAL_ACTION_DECREASE"} {
		if actions.ByName(protoreflect.Name(name)) == nil {
			t.Fatalf("missing signal action %q", name)
		}
	}
	for _, name := range []string{"SIGNAL_ACTION_ENTER", "SIGNAL_ACTION_EXIT"} {
		if actions.ByName(protoreflect.Name(name)) != nil {
			t.Fatalf("obsolete signal action %q is still registered", name)
		}
	}
	fields := (&tradingpb.TradingSignal{}).ProtoReflect().Descriptor().Fields()
	for _, name := range []string{"confidence", "strength"} {
		if fields.ByName(protoreflect.Name(name)) != nil {
			t.Fatalf("obsolete trading signal field %q is still registered", name)
		}
	}
}

func TestValidateTradingSignalRejectsUnknownAction(t *testing.T) {
	signal := &tradingpb.TradingSignal{
		StrategyId: "strategy-1", SignalId: "signal-1", Symbol: "BTC-USDT",
		Side: tradingpb.SignalSide_SIGNAL_SIDE_BUY, Action: tradingpb.SignalAction(99),
		SignalTime: timestamppb.New(time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)),
	}
	if err := ValidateTradingSignal(signal); err == nil {
		t.Fatal("ValidateTradingSignal() accepted an unknown action")
	}
}

func mustMarshal(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
