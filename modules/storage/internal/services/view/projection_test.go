package view

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestProjectionBatchesSecondaryReads(t *testing.T) {
	const rowCount = 500
	rows := make([]*pb.TimeSeriesRow, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		rows = append(rows, projectionTimeRow("primary", fmt.Sprintf("S%03d", i), "close"))
	}
	columns := []*pb.ViewColumn{
		projectionViewColumn("close", "primary.close"),
		projectionViewColumn("volume", "secondary_a.volume"),
		projectionViewColumn("score", "secondary_b.score"),
	}
	calls := map[string]int{}
	reader := func(_ context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
		if len(keys) == 0 {
			t.Fatal("batch reader called without keys")
		}
		datasetID := keys[0].GetDatasetId()
		calls[datasetID]++
		out := make([]*pb.TimeSeriesRow, 0, len(keys))
		for _, key := range keys {
			if key.GetDatasetId() != datasetID {
				t.Fatalf("mixed dataset batch: %s and %s", datasetID, key.GetDatasetId())
			}
			name := "volume"
			if datasetID == "secondary_b" {
				name = "score"
			}
			out = append(out, projectionTimeRow(datasetID, key.GetSubjectId(), name))
		}
		return out, nil
	}

	projected, ok, err := TimeSeriesRowsForView(context.Background(), &pb.View{
		SpaceId: "crypto", ViewId: "combined", PrimaryDatasetId: "primary",
	}, columns, rows, reader)
	if err != nil || !ok {
		t.Fatalf("TimeSeriesRowsForView: ok=%v err=%v", ok, err)
	}
	if len(projected) != rowCount {
		t.Fatalf("projected rows = %d, want %d", len(projected), rowCount)
	}
	if calls["secondary_a"] != 1 || calls["secondary_b"] != 1 {
		t.Fatalf("secondary read calls = %v, want one call per dataset", calls)
	}
	if len(projected[0].GetColumns()) != 3 {
		t.Fatalf("projected columns = %d, want 3", len(projected[0].GetColumns()))
	}
}

func projectionTimeRow(datasetID string, subjectID string, columnName string) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: datasetID, SubjectId: subjectID,
			Freq: "1m", DataTime: "2026-07-10T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: columnName, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}},
		}},
	}
}

func projectionViewColumn(name string, origin string) *pb.ViewColumn {
	return &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "combined", ColumnName: name,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   origin, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
}
