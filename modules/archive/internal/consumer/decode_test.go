package consumer

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"testing"
	"time"
)

func TestDecoderBuildsMonthlyPatches(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.RowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*storagepb.RowFieldUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-30T23:59:00Z"}}}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 100.25}}}}}}}
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload, "m1", event.GetSpaceId(), event.GetDatasetId()))
	if err != nil || decision != DecisionArchive || len(batch.Rows) != 1 {
		t.Fatalf("Decode() = %#v, %d, %v", batch, decision, err)
	}
	if batch.Rows[0].Partition.Month != "202606" || batch.Rows[0].Partition.SubjectID != "BTC-USDT" {
		t.Fatalf("unexpected partition: %#v", batch.Rows[0].Partition)
	}
}

func TestDecoderRejectsWholeEventWhenOneRowIsInvalid(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.RowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*storagepb.RowFieldUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "not-time"}}}}}}
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload, "m2", event.GetSpaceId(), event.GetDatasetId()))
	if err == nil || decision != DecisionReject || len(batch.Rows) != 0 {
		t.Fatalf("Decode() = %#v, %d, %v", batch, decision, err)
	}
}

func TestDecoderIgnoresUnknownSource(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.RowsUpserted{SpaceId: "stock_us", DatasetId: "equity_kline"}
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload, "m3", event.GetSpaceId(), event.GetDatasetId()))
	if err != nil || decision != DecisionIgnore || len(batch.Rows) != 0 {
		t.Fatalf("Decode() = %#v, %d, %v", batch, decision, err)
	}
}

func TestDecoderRejectsWrongStorageContentType(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.RowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline"}
	payload, _ := proto.Marshal(event)
	message := fixtureEnvelope(payload, "m4", event.GetSpaceId(), event.GetDatasetId())
	message.ContentType = "application/x-protobuf"

	batch, decision, err := decoder.Decode(message)
	if err == nil || decision != DecisionReject || len(batch.Rows) != 0 {
		t.Fatalf("Decode() = %#v, %d, %v, want content-type rejection", batch, decision, err)
	}
}

func TestDecoderRejectsMalformedStorageTopicAndPayloadMismatch(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := &storagepb.RowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline"}
	payload, err := proto.Marshal(event)
	require.NoError(t, err)
	base := fixtureEnvelope(payload, "m5", event.GetSpaceId(), event.GetDatasetId())
	for name, topic := range map[string]string{
		"one token":    "moox.storage.rows_upserted.v1.mzxw6",
		"three tokens": "moox.storage.rows_upserted.v1.mzxw6.mjqxe.extra",
		"wildcard":     "moox.storage.rows_upserted.v1.mzxw6.>",
	} {
		t.Run(name, func(t *testing.T) {
			message := proto.Clone(base).(*messagepb.MooxMessage)
			message.Topic = topic
			_, decision, err := decoder.Decode(message)
			if err == nil || decision != DecisionReject {
				t.Fatalf("Decode() decision=%d err=%v, want rejection", decision, err)
			}
		})
	}
	message := proto.Clone(base).(*messagepb.MooxMessage)
	message.MessageType = "moox.storage.other.v1"
	_, decision, err := decoder.Decode(message)
	if err == nil || decision != DecisionReject {
		t.Fatalf("message type Decode() decision=%d err=%v, want rejection", decision, err)
	}
	message = proto.Clone(base).(*messagepb.MooxMessage)
	message.ProtocolVersion = 99
	_, decision, err = decoder.Decode(message)
	if err == nil || decision != DecisionReject {
		t.Fatalf("protocol Decode() decision=%d err=%v, want rejection", decision, err)
	}
	message = proto.Clone(base).(*messagepb.MooxMessage)
	message.Kind = messagepb.MessageKind_MESSAGE_KIND_COMMAND
	_, decision, err = decoder.Decode(message)
	if err == nil || decision != DecisionReject {
		t.Fatalf("kind Decode() decision=%d err=%v, want rejection", decision, err)
	}
	message = proto.Clone(base).(*messagepb.MooxMessage)
	message.Payload, err = proto.Marshal(&storagepb.RowsUpserted{SpaceId: "stock_us", DatasetId: "equity_kline"})
	require.NoError(t, err)
	_, decision, err = decoder.Decode(message)
	if err == nil || decision != DecisionReject {
		t.Fatalf("mismatch Decode() decision=%d err=%v, want rejection", decision, err)
	}
}

func fixtureEnvelope(payload []byte, messageID, spaceID, datasetID string) *messagepb.MooxMessage {
	now := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	spaceToken, err := jetstream.EncodeSubjectToken(spaceID)
	if err != nil {
		panic(err)
	}
	datasetToken, err := jetstream.EncodeSubjectToken(datasetID)
	if err != nil {
		panic(err)
	}
	topic := fmt.Sprintf("%s%s.%s", jetstream.StorageRowsUpsertedTopicPrefix, spaceToken, datasetToken)
	return &messagepb.MooxMessage{ProtocolVersion: 1, MessageId: messageID, Topic: topic, Kind: messagepb.MessageKind_MESSAGE_KIND_EVENT, Producer: &messagepb.Producer{ServiceName: "archive-test", InstanceId: "test"}, OccurredAt: now, PublishedAt: now, ContentType: jetstream.StorageRowsUpsertedContentType, MessageType: jetstream.StorageRowsUpsertedMessageType, Payload: payload, SpaceId: spaceID}
}

func TestParseTime(t *testing.T) {
	ts, err := parseTime("2026-01-02T03:04:05Z")
	require.NoError(t, err)
	assert.Equal(t, time.UTC, ts.Location())
	_, err = parseTime("")
	require.Error(t, err)
}

func TestMergePatch(t *testing.T) {
	a := domain.RowPatch{
		Attributes: map[string]string{"source": "live"},
		Columns:    map[string]domain.Scalar{"open": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	b := domain.RowPatch{
		Attributes: map[string]string{"batch": "2"},
		Columns:    map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
		WrittenAt:  time.Unix(2, 0).UTC(),
	}
	merged := mergePatch(a, b)
	assert.Equal(t, "live", merged.Attributes["source"])
	assert.Equal(t, "2", merged.Attributes["batch"])
	assert.Contains(t, merged.Columns, "close")
	assert.Equal(t, b.WrittenAt, merged.WrittenAt)
}
