package duckdb_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestViewStoreCreatesInsertsAndQueriesRows(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_1", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_1", []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00+08:00", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
	}))

	gotColumns, rows, page, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_1", &pb.QueryTimeSeriesRowsReq{
		Keys:      []*pb.TimeSeriesKey{{SubjectId: "APT-USDT"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:00:00+08:00", EndTime: "2026-06-15T00:00:00+08:00"},
	})
	require.NoError(t, err)
	require.Len(t, gotColumns, 1)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Equal(t, "APT-USDT", rows[0].GetKey().GetSubjectId())
}

func TestViewStorePersistsResultTableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "views.duckdb")
	columns := []*pb.ViewColumn{{
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}}

	store, err := duckdb.Open(duckdb.Options{Path: path})
	require.NoError(t, err)
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_persist", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_persist", []*pb.TimeSeriesRow{
		timeSeriesRow("BTC-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 68000)}),
	}))
	require.NoError(t, store.Close())

	reopened, err := duckdb.Open(duckdb.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	tables, err := reopened.ListResultTables(ctx)
	require.NoError(t, err)
	require.Contains(t, tables, "view_result_crypto_kline_persist")
	gotColumns, rows, page, err := reopened.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_persist", &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT"}},
	})
	require.NoError(t, err)
	require.Len(t, gotColumns, 1)
	require.Len(t, rows, 1)
	require.Equal(t, uint32(1), page.GetTotal())
	require.InDelta(t, 68000, rows[0].GetColumns()[0].GetValue().GetDoubleValue(), 1e-9)
}

func TestViewStoreMergesDuplicateRowsOnInitialInsert(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{
		{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_initial_merge", columns))
	key := &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-06-15T00:00:00Z"}
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_initial_merge", []*pb.TimeSeriesRow{
		{Key: key, Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 68000)}},
		{Key: key, Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}, testutil.DoubleValue("volume", 12.5)}},
	}))

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_initial_merge", &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT"}},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Len(t, rows, 1)
	values := map[string]float64{}
	for _, column := range rows[0].GetColumns() {
		values[column.GetColumnName()] = column.GetValue().GetDoubleValue()
	}
	require.InDelta(t, 68000, values["close"], 1e-9)
	require.InDelta(t, 12.5, values["volume"], 1e-9)
}

func TestViewStoreAppliesClosedTimeRangeWithTimeZones(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_time", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_time", []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00+08:00", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
	}))

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_time", &pb.QueryTimeSeriesRowsReq{
		TimeRange: &pb.TimeRange{StartTime: "2026-06-14T15:59:59Z", EndTime: "2026-06-14T16:00:00Z"},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Len(t, rows, 1)
}

func TestViewStoreFiltersByCompleteTimeSeriesKey(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_key", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_key", []*pb.TimeSeriesRow{
		timeSeriesRowWithKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"adj": "none"}, DataTime: "2026-06-15T00:00:00Z"}, 8.1),
		timeSeriesRowWithKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "5m", Dimensions: map[string]string{"adj": "none"}, DataTime: "2026-06-15T00:00:00Z"}, 8.2),
		timeSeriesRowWithKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"adj": "qfq"}, DataTime: "2026-06-15T00:00:00Z"}, 8.3),
		timeSeriesRowWithKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"adj": "none"}, DataTime: "2026-06-15T00:01:00Z"}, 8.4),
		timeSeriesRowWithKey(&pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "other_kline", SubjectId: "APT-USDT", Freq: "1m", Dimensions: map[string]string{"adj": "none"}, DataTime: "2026-06-15T00:00:00Z"}, 8.5),
	}))

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_key", &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:    "crypto",
			DatasetId:  "kline",
			SubjectId:  "APT-USDT",
			Freq:       "1m",
			Dimensions: map[string]string{"adj": "none"},
			DataTime:   "2026-06-15T00:00:00Z",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Len(t, rows, 1)
	require.InDelta(t, 8.1, rows[0].GetColumns()[0].GetValue().GetDoubleValue(), 1e-9)
}

func TestViewStoreAppliesProjectionFiltersAndSorts(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{
		{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_filter", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_filter", []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00+08:00", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1), testutil.DoubleValue("volume", 100)}),
		timeSeriesRow("AR-USDT", "2026-06-15T00:00:00+08:00", []*pb.ColumnValue{testutil.DoubleValue("close", 3.2), testutil.DoubleValue("volume", 200)}),
		timeSeriesRow("OP-USDT", "2026-06-15T00:00:00+08:00", []*pb.ColumnValue{testutil.DoubleValue("close", 10.5), testutil.DoubleValue("volume", 50)}),
	}))

	gotColumns, rows, page, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_filter", &pb.QueryTimeSeriesRowsReq{
		ColumnNames: []string{"close"},
		Filters: []*pb.FilterExpr{{
			Expr: "close >= $min_close",
			Args: map[string]*pb.TypedValue{"min_close": {Value: &pb.TypedValue_DoubleValue{DoubleValue: 8}}},
		}},
		Sorts: []*pb.SortSpec{{FieldName: "volume", Desc: true}},
	})
	require.NoError(t, err)
	require.Len(t, gotColumns, 1)
	require.Equal(t, uint32(2), page.GetTotal())
	require.Len(t, rows, 2)
	require.Equal(t, "APT-USDT", rows[0].GetKey().GetSubjectId())
	require.Equal(t, "OP-USDT", rows[1].GetKey().GetSubjectId())
	require.Len(t, rows[0].GetColumns(), 1)
	require.Equal(t, "close", rows[0].GetColumns()[0].GetColumnName())
}

func TestViewStoreRejectsUnsupportedFilterExpression(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_bad_filter", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_bad_filter", []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
	}))

	_, _, _, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_bad_filter", &pb.QueryTimeSeriesRowsReq{
		Filters: []*pb.FilterExpr{{Expr: "unsupported(close)"}},
	})
	require.ErrorContains(t, err, "unsupported filter expression")
}

func TestViewStoreAppliesStringFunctionFilters(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{
		{ColumnName: "symbol", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.symbol", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "note", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.note", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_string_filter", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_string_filter", []*pb.TimeSeriesRow{
		timeSeriesRow("BTC-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.StringValue("symbol", "BTC-USDT"), testutil.StringValue("note", "tradeable")}),
		timeSeriesRow("ETH-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.StringValue("symbol", "ETH-USDT"), testutil.StringValue("note", "test-only")}),
		timeSeriesRow("BNB-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.StringValue("symbol", "BNB-USDT")}),
	}))

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_string_filter", &pb.QueryTimeSeriesRowsReq{
		Filters: []*pb.FilterExpr{
			{Expr: "starts_with(symbol, $prefix)", Args: map[string]*pb.TypedValue{"prefix": {Value: &pb.TypedValue_StringValue{StringValue: "B"}}}},
			{Expr: "ends_with(symbol, $suffix)", Args: map[string]*pb.TypedValue{"suffix": {Value: &pb.TypedValue_StringValue{StringValue: "USDT"}}}},
			{Expr: "not_contains(note, $blocked)", Args: map[string]*pb.TypedValue{"blocked": {Value: &pb.TypedValue_StringValue{StringValue: "test"}}}},
		},
		Sorts: []*pb.SortSpec{{FieldName: "symbol"}},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(2), page.GetTotal())
	require.Len(t, rows, 2)
	require.Equal(t, "BNB-USDT", rows[0].GetKey().GetSubjectId())
	require.Equal(t, "BTC-USDT", rows[1].GetKey().GetSubjectId())

	_, emptyRows, emptyPage, err := store.QueryTimeSeriesRows(ctx, "view_result_crypto_kline_string_filter", &pb.QueryTimeSeriesRowsReq{
		Filters: []*pb.FilterExpr{{Expr: "is_empty(note)"}},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), emptyPage.GetTotal())
	require.Len(t, emptyRows, 1)
	require.Equal(t, "BNB-USDT", emptyRows[0].GetKey().GetSubjectId())
}

func TestViewStoreDropsResultTableAndColumns(t *testing.T) {
	ctx := context.Background()
	store := openViewStore(t)
	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_drop", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_drop", []*pb.TimeSeriesRow{
		timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
	}))

	tables, err := store.ListResultTables(ctx)
	require.NoError(t, err)
	require.Contains(t, tables, "view_result_crypto_kline_drop")

	require.NoError(t, store.DropResultTable(ctx, "view_result_crypto_kline_drop"))
	tables, err = store.ListResultTables(ctx)
	require.NoError(t, err)
	require.NotContains(t, tables, "view_result_crypto_kline_drop")
}

func openViewStore(t *testing.T) *duckdb.ViewStore {
	t.Helper()
	store, err := duckdb.Open(duckdb.Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func timeSeriesRow(subjectID string, dataTime string, columns []*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: subjectID, Freq: "1m", DataTime: dataTime},
		Columns: columns,
	}
}

func timeSeriesRowWithKey(key *pb.TimeSeriesKey, close float64) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key:     key,
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", close)},
	}
}
