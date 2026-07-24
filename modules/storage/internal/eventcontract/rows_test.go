package eventcontract

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
			{Key: &localpb.RowKey{SpaceId: "crypto", DatasetId: "prices", Kind: &localpb.RowKey_TimeSeries{TimeSeries: &localpb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-23T00:00:00Z"}}}, Fields: []*localpb.FieldValue{{FieldId: "close", Value: &localpb.TypedValue{Value: &localpb.TypedValue_DoubleValue{DoubleValue: 101.25}}}}, Attributes: map[string]*localpb.TypedValue{"source": {Value: &localpb.TypedValue_StringValue{StringValue: "binance"}}}, Operation: localpb.RowFieldOperation_ROW_FIELD_OPERATION_UPSERT},
			{Key: &localpb.RowKey{SpaceId: "crypto", DatasetId: "prices", Kind: &localpb.RowKey_Record{Record: &localpb.RecordRowKey{RecordId: "r-1", Version: "v1"}}}, Fields: []*localpb.FieldValue{{FieldId: "payload", Value: &localpb.TypedValue{Value: &localpb.TypedValue_BytesValue{BytesValue: []byte{1, 2, 3}}}}}, Operation: localpb.RowFieldOperation_ROW_FIELD_OPERATION_UNSPECIFIED},
		},
	}
	shared, err := ToSharedRows(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared.GetRows()) != 2 || shared.GetRows()[0].GetKey().GetTimeSeries().GetSubjectId() != "BTC-USDT" || shared.GetRows()[1].GetKey().GetRecord().GetRecordId() != "r-1" {
		t.Fatalf("shared rows lost key structure: %v", shared)
	}
	if shared.GetRows()[0].GetAttributes()["source"].GetStringValue() != "binance" || shared.GetRows()[1].GetFields()[0].GetValue().GetBytesValue()[2] != 3 {
		t.Fatalf("shared rows lost typed values: %v", shared)
	}

	local, err := ToLocalRows(shared)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range local.GetRows() {
		if row.GetOperation() != localpb.RowFieldOperation_ROW_FIELD_OPERATION_UPSERT {
			t.Fatalf("row %d operation = %s, want UPSERT", i, row.GetOperation())
		}
	}
}

func TestToSharedRowsRejectsRowIdentityMismatch(t *testing.T) {
	_, err := ToSharedRows(&localpb.RowsUpserted{SpaceId: "crypto", DatasetId: "prices", Rows: []*localpb.RowFieldUpsert{{Key: &localpb.RowKey{SpaceId: "other", DatasetId: "prices"}}}})
	if err == nil {
		t.Fatal("row identity mismatch was accepted")
	}
	_, err = ToLocalRows(&sharedpb.DatasetRowsUpserted{SpaceId: "crypto", DatasetId: "prices", Rows: []*sharedpb.RowUpsert{{Key: &sharedpb.RowKey{SpaceId: "other", DatasetId: "prices"}}}})
	if err == nil {
		t.Fatal("invalid shared rows were accepted")
	}
}
