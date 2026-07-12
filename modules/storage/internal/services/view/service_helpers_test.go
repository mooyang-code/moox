package view

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestRequestedViewQueryFields_DedupesFields(t *testing.T) {
	got := requestedViewQueryFields(
		[]string{"open", "close"},
		[]*pb.FilterExpr{{Expr: "close > 0"}, {Expr: "contains(symbol, BTC)"}},
		[]*pb.SortSpec{{FieldName: "open"}},
	)
	assert.Equal(t, []string{"open", "close", "symbol"}, got)
}

func TestViewFilterField_ParsesExpressions(t *testing.T) {
	assert.Equal(t, "close", viewFilterField("close > 0"))
	assert.Equal(t, "symbol", viewFilterField("contains(symbol, BTC)"))
	assert.Equal(t, "price", viewFilterField("max(price, 1)"))
	assert.Equal(t, "", viewFilterField(""))
}

func TestNormalizeRecordSearchKeys_FillsDefaults(t *testing.T) {
	keys, err := normalizeRecordSearchKeys("crypto", "ds-1", []*pb.RecordKey{{RecordId: "r1"}})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "crypto", keys[0].GetSpaceId())
	assert.Equal(t, "ds-1", keys[0].GetDatasetId())
}

func TestNormalizeRecordSearchKeys_RejectsMismatchedSpace(t *testing.T) {
	_, err := normalizeRecordSearchKeys("crypto", "ds-1", []*pb.RecordKey{
		{SpaceId: "other", DatasetId: "ds-1", RecordId: "r1"},
	})
	require.Error(t, err)
}

func TestProjectRecordRowColumns_FiltersColumns(t *testing.T) {
	row := &pb.RecordRow{
		Columns: []*pb.ColumnValue{
			{ColumnName: "a"},
			{ColumnName: "b"},
		},
	}
	got := projectRecordRowColumns(row, []string{"b"})
	require.Len(t, got.GetColumns(), 1)
	assert.Equal(t, "b", got.GetColumns()[0].GetColumnName())
}

func TestProjectResultColumns_FiltersByIncludes(t *testing.T) {
	columns := []*pb.ViewColumn{
		{ColumnName: "open", OriginId: "kline.open"},
		{ColumnName: "close", OriginId: "kline.close"},
	}
	got := projectResultColumns(columns, []string{"open"})
	require.Len(t, got, 1)
	assert.Equal(t, "open", got[0].GetColumnName())
	assert.Equal(t, "kline", got[0].GetDatasetId())
}

func TestViewColumnDatasetID_ParsesOrigin(t *testing.T) {
	assert.Equal(t, "kline", viewColumnDatasetID(&pb.ViewColumn{OriginId: "kline.open"}))
	assert.Equal(t, "", viewColumnDatasetID(&pb.ViewColumn{OriginId: "kline"}))
}

func TestProjectRecordRowColumns_ReturnsOriginalWhenIncludesEmpty(t *testing.T) {
	row := &pb.RecordRow{Columns: []*pb.ColumnValue{{ColumnName: "a"}}}
	got := projectRecordRowColumns(row, nil)
	assert.True(t, proto.Equal(row, got))
}
