package deriver

import (
	"context"
	"reflect"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestProjectColumnsForTimeSeriesViewUsesQualifiedOrigin(t *testing.T) {
	columns := []*pb.ViewColumn{
		{
			ColumnName: "swap.close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "swap_kline.close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		},
	}
	rowsByDataset := map[string]*pb.TimeSeriesRow{
		"swap_kline": {
			Columns: []*pb.ColumnValue{
				doubleColumn("close", 12.3),
			},
		},
	}

	got := ProjectColumnsForView("spot_kline", columns, rowsByDataset)
	want := []*pb.ColumnValue{
		doubleColumn("swap.close", 12.3),
	}
	if !equalColumns(got, want) {
		t.Fatalf("projected columns = %#v, want %#v", got, want)
	}
}

func TestRecordRowsForViewReadsRelatedDataset(t *testing.T) {
	ctx := context.Background()
	item := &pb.View{PrimaryDatasetId: "profile"}
	columns := []*pb.ViewColumn{
		{
			ColumnName: "name",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "profile.name",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		},
		{
			ColumnName: "score",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "scorecard.score",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		},
	}
	primary := &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   "demo",
			DatasetId: "profile",
			RecordId:  "u1",
			Version:   "v1",
		},
		Columns: []*pb.ColumnValue{
			stringColumn("name", "alice"),
		},
		Attributes: map[string]string{"source": "primary"},
	}
	related := &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   "demo",
			DatasetId: "scorecard",
			RecordId:  "u1",
			Version:   "v1",
		},
		Columns: []*pb.ColumnValue{
			intColumn("score", 98),
		},
	}

	got, ok, err := RecordRowsForView(ctx, item, columns, []*pb.RecordRow{primary}, func(ctx context.Context, key *pb.RecordKey, datasetID string) (*pb.RecordRow, error) {
		if datasetID != "scorecard" {
			t.Fatalf("datasetID = %q, want scorecard", datasetID)
		}
		if key.GetDatasetId() != "profile" || key.GetRecordId() != "u1" || key.GetVersion() != "v1" {
			t.Fatalf("base key = %#v", key)
		}
		return related, nil
	})
	if err != nil {
		t.Fatalf("RecordRowsForView error = %v", err)
	}
	if !ok {
		t.Fatal("RecordRowsForView ok = false, want true")
	}
	if len(got) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(got))
	}
	wantColumns := []*pb.ColumnValue{
		stringColumn("name", "alice"),
		intColumn("score", 98),
	}
	if !equalColumns(got[0].GetColumns(), wantColumns) {
		t.Fatalf("projected columns = %#v, want %#v", got[0].GetColumns(), wantColumns)
	}
	if !reflect.DeepEqual(got[0].GetAttributes(), primary.GetAttributes()) {
		t.Fatalf("attributes = %#v, want %#v", got[0].GetAttributes(), primary.GetAttributes())
	}
}

func equalColumns(a, b []*pb.ColumnValue) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if !proto.Equal(a[idx], b[idx]) {
			return false
		}
	}
	return true
}

func stringColumn(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value: &pb.TypedValue{
			Value: &pb.TypedValue_StringValue{StringValue: value},
		},
	}
}

func intColumn(name string, value int64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value: &pb.TypedValue{
			Value: &pb.TypedValue_IntValue{IntValue: value},
		},
	}
}

func doubleColumn(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value: &pb.TypedValue{
			Value: &pb.TypedValue_DoubleValue{DoubleValue: value},
		},
	}
}
