package view

import (
	"context"
	"fmt"
	"testing"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestRecordRowsForViewProjectsAcrossDatasets(t *testing.T) {
	view := &pb.View{SpaceId: "crypto", ViewId: "combined", PrimaryDatasetId: "primary"}
	columns := []*pb.ViewColumn{
		projectionViewColumn("title", "primary.title"),
		projectionViewColumn("score", "secondary.score"),
	}
	rows := []*pb.RecordRow{{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "primary", RecordId: "news-1", Version: "2026-07-11T00:00:00Z"},
		Columns: []*pb.ColumnValue{{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}},
	}}
	reader := func(_ context.Context, keys []*pb.RecordKey) ([]*pb.RecordRow, error) {
		require.Len(t, keys, 1)
		return []*pb.RecordRow{{
			Key:     keys[0],
			Columns: []*pb.ColumnValue{{ColumnName: "score", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
		}}, nil
	}
	projected, ok, err := RecordRowsForView(context.Background(), view, columns, rows, reader)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, projected, 1)
	require.Len(t, projected[0].GetColumns(), 2)
}

func TestProjectionHelpersExposeOriginMetadata(t *testing.T) {
	column := &pb.ViewColumn{OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ColumnName: "close"}
	assert.Equal(t, "kline", ViewColumnOriginDataset("primary", column))
	assert.Equal(t, "close", ViewColumnSourceName("kline", column))
	assert.Equal(t, []string{"primary", "kline"}, ViewProjectionDatasets("primary", []*pb.ViewColumn{column}))
}

func TestIsProjectableViewRejectsExpressionColumns(t *testing.T) {
	view := &pb.View{PrimaryDatasetId: "primary"}
	columns := []*pb.ViewColumn{{OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_EXPRESSION, OriginId: "expr"}}
	assert.False(t, IsProjectableTimeSeriesView(view, columns))
	assert.False(t, IsProjectableRecordView(view, columns))
}

func TestCloneStringMapCopiesEntries(t *testing.T) {
	cloned := CloneStringMap(map[string]string{"a": "1"})
	require.NotNil(t, cloned)
	assert.Equal(t, "1", cloned["a"])
}

func TestTimeSeriesRowsForViewReturnsReaderError(t *testing.T) {
	view := &pb.View{PrimaryDatasetId: "primary"}
	columns := []*pb.ViewColumn{
		projectionViewColumn("close", "primary.close"),
		projectionViewColumn("volume", "secondary.volume"),
	}
	rows := []*pb.TimeSeriesRow{projectionTimeRow("primary", "BTC", "close")}
	_, ok, err := TimeSeriesRowsForView(context.Background(), view, columns, rows, func(context.Context, []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
		return nil, errors.New("read failed")
	})
	require.Error(t, err)
	assert.True(t, ok)
}
