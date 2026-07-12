package view

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
