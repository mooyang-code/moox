package storagepb

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

func TestDatasetRowsUpsertedRoundTrip(t *testing.T) {
	in := &DatasetRowsUpserted{
		SpaceId:   "crypto",
		DatasetId: "spot_kline",
		Rows: []*RowUpsert{
			{Key: &RowKey{SpaceId: "crypto", DatasetId: "spot_kline", Kind: &RowKey_TimeSeries{TimeSeries: &TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-23T00:00:00Z", SeriesTag: "venue:okx"}}}, Fields: []*FieldValue{{FieldId: "close", Value: &TypedValue{Value: &TypedValue_DoubleValue{DoubleValue: 101.25}}}}, Attributes: map[string]*TypedValue{"source": {Value: &TypedValue_StringValue{StringValue: "binance"}}}},
			{Key: &RowKey{SpaceId: "crypto", DatasetId: "spot_kline", Kind: &RowKey_Record{Record: &RecordRowKey{RecordId: "r-1", Version: "v1"}}}, Fields: []*FieldValue{{FieldId: "payload", Value: &TypedValue{Value: &TypedValue_BytesValue{BytesValue: []byte{1, 2, 3}}}}, {FieldId: "tags", Value: &TypedValue{Value: &TypedValue_ListValue{ListValue: &ValueList{Values: []*TypedValue{{Value: &TypedValue_StringValue{StringValue: "a"}}, {Value: &TypedValue_NullValue{NullValue: NullValue_NULL_VALUE_NULL}}}}}}}}},
		},
	}
	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	out := new(DatasetRowsUpserted)
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(in, out) {
		t.Fatalf("round-trip changed payload: in=%v out=%v", in, out)
	}
}
