package eventmapper

import (
	"testing"

	localpb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
)

func TestRowsBoundaryConversionPreservesStructuredDelta(t *testing.T) {
	in := &localpb.RowsUpserted{
		SpaceId:   "crypto",
		DatasetId: "prices",
		Rows: []*localpb.RowFieldUpsert{
			{Key: &localpb.RowKey{SpaceId: "crypto", DatasetId: "prices", Kind: &localpb.RowKey_TimeSeries{TimeSeries: &localpb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-23T00:00:00Z", Dimensions: map[string]string{"venue": "binance"}}}}, Fields: []*localpb.FieldValue{{FieldId: "close", Value: &localpb.TypedValue{Value: &localpb.TypedValue_DoubleValue{DoubleValue: 101.25}}}}, Attributes: map[string]*localpb.TypedValue{"source": {Value: &localpb.TypedValue_StringValue{StringValue: "binance"}}}},
			{Key: &localpb.RowKey{SpaceId: "crypto", DatasetId: "prices", Kind: &localpb.RowKey_Record{Record: &localpb.RecordRowKey{RecordId: "r-1", Version: "v1"}}}, Fields: []*localpb.FieldValue{{FieldId: "payload", Value: &localpb.TypedValue{Value: &localpb.TypedValue_BytesValue{BytesValue: []byte{1, 2, 3}}}}}},
		},
	}
	shared, err := ToEventRows(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.GetRows()) != 2 || shared.GetRows()[0].GetKey().GetTimeSeries().GetSubjectId() != "BTC-USDT" || shared.GetRows()[1].GetKey().GetRecord().GetRecordId() != "r-1" {
		t.Fatalf("shared rows lost key structure: %v", shared)
	}
	if shared.GetRows()[0].GetAttributes()["source"].GetStringValue() != "binance" || shared.GetRows()[1].GetFields()[0].GetValue().GetBytesValue()[2] != 3 {
		t.Fatalf("shared rows lost typed values: %v", shared)
	}
	if shared.GetRows()[0].GetKey().GetTimeSeries().GetDimensions()["venue"] != "binance" {
		t.Fatalf("shared rows lost dimensions: %v", shared)
	}

	local, err := ToStorageRows(shared)
	if err != nil {
		t.Fatal(err)
	}
	if local.GetRows()[0].GetKey().GetTimeSeries().GetDimensions()["venue"] != "binance" {
		t.Fatalf("local rows lost dimensions: %v", local)
	}
}

func TestToEventRowsRejectsRowIdentityMismatch(t *testing.T) {
	_, err := ToEventRows(&localpb.RowsUpserted{SpaceId: "crypto", DatasetId: "prices", Rows: []*localpb.RowFieldUpsert{{Key: &localpb.RowKey{SpaceId: "other", DatasetId: "prices"}}}})
	if err == nil {
		t.Fatal("row identity mismatch was accepted")
	}
	_, err = ToStorageRows(&sharedpb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "prices", Rows: []*sharedpb.RowUpsert{{Key: &sharedpb.RowKey{SpaceId: "other", DatasetId: "prices"}}}})
	if err == nil {
		t.Fatal("invalid shared rows were accepted")
	}
}
