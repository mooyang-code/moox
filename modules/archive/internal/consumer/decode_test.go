package consumer

import (
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

func TestDecoderBuildsMonthlyPatches(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.TimeSeriesRowsUpdated{MessageId: "m1", WrittenAt: "2026-07-01T00:00:00Z", SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-30T23:59:00Z"}, Columns: []*storagepb.ColumnValue{{ColumnName: "close", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 100.25}}}}}}}
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload, "m1"))
	if err != nil || decision != DecisionArchive || len(batch.Rows) != 1 {
		t.Fatalf("Decode() = %#v, %d, %v", batch, decision, err)
	}
	if batch.Rows[0].Partition.Month != "202606" || batch.Rows[0].Partition.SubjectID != "BTC-USDT" {
		t.Fatalf("unexpected partition: %#v", batch.Rows[0].Partition)
	}
}

func TestDecoderRejectsWholeEventWhenOneRowIsInvalid(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.TimeSeriesRowsUpdated{MessageId: "m2", WrittenAt: "2026-07-01T00:00:00Z", SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*storagepb.TimeSeriesRow{{Key: &storagepb.TimeSeriesKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "not-time"}}}}
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload, "m2"))
	if err == nil || decision != DecisionReject || len(batch.Rows) != 0 {
		t.Fatalf("Decode() = %#v, %d, %v", batch, decision, err)
	}
}

func TestDecoderIgnoresUnknownSource(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.TimeSeriesRowsUpdated{MessageId: "m3", WrittenAt: "2026-07-01T00:00:00Z", SpaceId: "stock_us", DatasetId: "equity_kline"}
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload, "m3"))
	if err != nil || decision != DecisionIgnore || len(batch.Rows) != 0 {
		t.Fatalf("Decode() = %#v, %d, %v", batch, decision, err)
	}
}

func fixtureEnvelope(payload []byte, messageID string) *messagepb.MooxMessage {
	return &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: messageID, Topic: "moox.storage.time_series.rows_updated.v1", Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, ContentType: "application/x-protobuf; message=trpc.moox.storage.TimeSeriesRowsUpdated", Payload: payload, SpaceId: "crypto_binance"}
}
