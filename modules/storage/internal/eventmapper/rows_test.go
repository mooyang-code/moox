package eventmapper

import (
	"testing"

	localpb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

func TestRowsBoundaryConversionPreservesSeriesTag(t *testing.T) {
	in := &localpb.RowsUpserted{
		SpaceId:   "crypto",
		DatasetId: "prices",
		Rows: []*localpb.RowFieldUpsert{
			{Key: &localpb.RowKey{SpaceId: "crypto", DatasetId: "prices", Kind: &localpb.RowKey_TimeSeries{TimeSeries: &localpb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-23T00:00:00Z", SeriesTag: "venue:okx"}}}, Fields: []*localpb.FieldValue{{FieldId: "close", Value: &localpb.TypedValue{Value: &localpb.TypedValue_DoubleValue{DoubleValue: 101.25}}}}, Attributes: map[string]*localpb.TypedValue{"source": {Value: &localpb.TypedValue_StringValue{StringValue: "binance"}}}},
			{Key: &localpb.RowKey{SpaceId: "crypto", DatasetId: "prices", Kind: &localpb.RowKey_Record{Record: &localpb.RecordRowKey{RecordId: "r-1", Version: "v1"}}}, Fields: []*localpb.FieldValue{{FieldId: "payload", Value: &localpb.TypedValue{Value: &localpb.TypedValue_BytesValue{BytesValue: []byte{1, 2, 3}}}}}},
		},
	}
	before, err := (proto.MarshalOptions{Deterministic: true}).Marshal(in)
	if err != nil {
		t.Fatal(err)
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
	if got := shared.GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag(); got != "venue:okx" {
		t.Fatalf("shared series_tag = %q", got)
	}

	local, err := ToStorageRows(shared)
	if err != nil {
		t.Fatal(err)
	}
	after, err := (proto.MarshalOptions{Deterministic: true}).Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("event mapper changed bytes: before=%x after=%x", before, after)
	}
}

func TestRowsBoundaryConversionPreservesDefaultSeries(t *testing.T) {
	in := &localpb.RowsUpserted{
		SpaceId: "crypto", DatasetId: "prices",
		Rows: []*localpb.RowFieldUpsert{{
			Key: &localpb.RowKey{
				SpaceId: "crypto", DatasetId: "prices",
				Kind: &localpb.RowKey_TimeSeries{TimeSeries: &localpb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-23T00:00:00Z",
				}},
			},
		}},
	}
	before, err := (proto.MarshalOptions{Deterministic: true}).Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := ToEventRows(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := shared.GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag(); got != "" {
		t.Fatalf("default series_tag = %q", got)
	}
	local, err := ToStorageRows(shared)
	if err != nil {
		t.Fatal(err)
	}
	after, err := (proto.MarshalOptions{Deterministic: true}).Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("default series changed bytes: before=%x after=%x", before, after)
	}
}

func TestTimeSeriesSelectorDistinguishesSeriesTagPresence(t *testing.T) {
	empty := ""
	value := "venue:okx"
	tests := []struct {
		name    string
		tag     *string
		present bool
		value   string
	}{
		{name: "absent"},
		{name: "present empty", tag: &empty, present: true},
		{name: "present value", tag: &value, present: true, value: value},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := &localpb.TimeSeriesSelector{SeriesTag: tt.tag}
			field := selector.ProtoReflect().Descriptor().Fields().ByName("series_tag")
			if got := selector.ProtoReflect().Has(field); got != tt.present {
				t.Fatalf("series_tag presence = %v, want %v", got, tt.present)
			}
			if got := selector.GetSeriesTag(); got != tt.value {
				t.Fatalf("series_tag value = %q, want %q", got, tt.value)
			}
		})
	}
}

func TestRowsBoundaryConversionPreservesExplicitNull(t *testing.T) {
	in := &localpb.RowsUpserted{
		SpaceId: "crypto", DatasetId: "prices",
		Rows: []*localpb.RowFieldUpsert{{
			Key: &localpb.RowKey{
				SpaceId: "crypto", DatasetId: "prices",
				Kind: &localpb.RowKey_Record{Record: &localpb.RecordRowKey{RecordId: "r-1", Version: "v1"}},
			},
			Fields: []*localpb.FieldValue{{
				FieldId: "close",
				Value: &localpb.TypedValue{Value: &localpb.TypedValue_NullValue{
					NullValue: localpb.NullValue_NULL_VALUE_NULL,
				}},
			}},
		}},
	}

	shared, err := ToEventRows(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := shared.GetRows()[0].GetFields()[0].GetValue().GetNullValue(); got != sharedpb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("shared null marker = %s", got)
	}
	local, err := ToStorageRows(shared)
	if err != nil {
		t.Fatal(err)
	}
	if got := local.GetRows()[0].GetFields()[0].GetValue().GetNullValue(); got != localpb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("local null marker = %s", got)
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
