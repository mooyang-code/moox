package eventconsumer

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDecoderBuildsMonthlyPatches(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := validStorageEvent()
	event.Rows[0].Key.GetTimeSeries().DataTime = "2026-06-30T23:59:00Z"
	raw, subject, messageID := marshalEvent(t, event)
	batch, decision, err := decoder.DecodeEvent(raw, subject, messageID)
	if err != nil || decision != DecisionArchive || len(batch.Rows) != 1 {
		t.Fatalf("DecodeEvent() = %#v, %d, %v", batch, decision, err)
	}
	assert.Equal(t, "202606", batch.Rows[0].Partition.Month)
}

func TestDecoderRejectsInvalidRow(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := validStorageEvent()
	event.Rows[0].Key.GetTimeSeries().DataTime = "not-time"
	raw, subject, messageID := marshalUncheckedEvent(t, event)
	batch, decision, err := decoder.DecodeEvent(raw, subject, messageID)
	assert.Error(t, err)
	assert.Equal(t, DecisionReject, decision)
	assert.Empty(t, batch.Rows)
}

func TestDecoderIgnoresUnknownSource(t *testing.T) {
	decoder := NewDecoder(map[string][]string{"crypto_binance": {"spot_kline"}})
	event := validStorageEvent()
	event.SpaceId = "stock_us"
	event.DatasetId = "equity_kline"
	for _, row := range event.Rows {
		row.Key.SpaceId = event.SpaceId
		row.Key.DatasetId = event.DatasetId
	}
	raw, subject, messageID := marshalEvent(t, event)
	batch, decision, err := decoder.DecodeEvent(raw, subject, messageID)
	require.NoError(t, err)
	assert.Equal(t, DecisionIgnore, decision)
	assert.Empty(t, batch.Rows)
}

func TestParseTimeAndMergePatch(t *testing.T) {
	ts, err := parseTime("2026-01-02T03:04:05Z")
	require.NoError(t, err)
	assert.Equal(t, time.UTC, ts.Location())
	_, err = parseTime("")
	require.Error(t, err)
	a := domain.RowPatch{Attributes: map[string]string{"source": "live"}, Columns: map[string]domain.Scalar{"open": {Type: 3}}}
	b := domain.RowPatch{Attributes: map[string]string{"batch": "2"}, Columns: map[string]domain.Scalar{"close": {Type: 3}}, WrittenAt: time.Unix(2, 0).UTC()}
	merged := mergePatch(a, b)
	assert.Equal(t, "live", merged.Attributes["source"])
	assert.Equal(t, "2", merged.Attributes["batch"])
	assert.Contains(t, merged.Columns, "close")
}

func validStorageEvent() *storagepb.DatasetRowsUpserted {
	return &storagepb.DatasetRowsUpserted{SpaceId: "crypto_binance", DatasetId: "spot_kline", Rows: []*storagepb.RowUpsert{{Key: &storagepb.RowKey{SpaceId: "crypto_binance", DatasetId: "spot_kline", Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-06-30T23:59:00Z"}}}, Fields: []*storagepb.FieldValue{{FieldId: "close", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 100.25}}}}}}}
}

func marshalEvent(t *testing.T, payload *storagepb.DatasetRowsUpserted) ([]byte, string, string) {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(events.DatasetRowsUpserted, payload, events.PublishOptions{EventID: "m1", OccurredAt: time.Now().UTC(), SpaceID: payload.GetSpaceId(), SubjectID: payload.GetDatasetId()})
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return raw, encoded.Subject, encoded.Message.GetEventId()
}

func marshalUncheckedEvent(t *testing.T, payload *storagepb.DatasetRowsUpserted) ([]byte, string, string) {
	t.Helper()
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	baseline := validStorageEvent()
	encoded, err := registry.Encode(events.DatasetRowsUpserted, baseline, events.PublishOptions{
		EventID: "m1", OccurredAt: time.Now().UTC(), SpaceID: baseline.GetSpaceId(), SubjectID: baseline.GetDatasetId(),
	})
	require.NoError(t, err)
	encoded.Message.Payload, err = proto.Marshal(payload)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	return raw, encoded.Subject, encoded.Message.GetEventId()
}
