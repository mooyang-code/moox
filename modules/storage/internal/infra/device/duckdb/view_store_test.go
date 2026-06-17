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
	store, err := duckdb.Open(duckdb.Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	require.NoError(t, err)
	defer store.Close()

	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_1", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_1", []*pb.QueryViewRow{{
		SubjectId: "APT-USDT",
		DataTime:  "2026-06-15T00:00:00+08:00",
		Values:    []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}}))

	gotColumns, rows, page, err := store.QueryView(ctx, "view_result_crypto_kline_1", &pb.QueryViewReq{
		SubjectIds: []string{"APT-USDT"},
		QueryTime: &pb.QueryTime{TimeRange: &pb.TimeRange{
			StartTime:      "2026-06-15T00:00:00+08:00",
			EndTime:        "2026-06-15T00:00:00+08:00",
			StartInclusive: true,
			EndInclusive:   true,
		}},
	})
	require.NoError(t, err)
	require.Len(t, gotColumns, 1)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Equal(t, "APT-USDT", rows[0].GetSubjectId())
}

func TestViewStoreAppliesTimeRangeWithTimeZonesAndInclusivity(t *testing.T) {
	ctx := context.Background()
	store, err := duckdb.Open(duckdb.Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	require.NoError(t, err)
	defer store.Close()

	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_time", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_time", []*pb.QueryViewRow{{
		SubjectId: "APT-USDT",
		DataTime:  "2026-06-15T00:00:00+08:00",
		Values:    []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}}))

	_, rows, page, err := store.QueryView(ctx, "view_result_crypto_kline_time", &pb.QueryViewReq{
		QueryTime: &pb.QueryTime{TimeRange: &pb.TimeRange{
			StartTime:      "2026-06-14T15:59:59Z",
			StartInclusive: false,
			EndTime:        "2026-06-14T16:00:00Z",
			EndInclusive:   true,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Len(t, rows, 1)

	_, rows, page, err = store.QueryView(ctx, "view_result_crypto_kline_time", &pb.QueryViewReq{
		QueryTime: &pb.QueryTime{TimeRange: &pb.TimeRange{
			StartTime:      "2026-06-14T15:59:59Z",
			StartInclusive: false,
			EndTime:        "2026-06-14T16:00:00Z",
			EndInclusive:   false,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(0), page.GetTotal())
	require.Empty(t, rows)
}

func TestViewStoreAppliesProjectionFiltersAndSorts(t *testing.T) {
	ctx := context.Background()
	store, err := duckdb.Open(duckdb.Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	require.NoError(t, err)
	defer store.Close()

	columns := []*pb.ViewColumn{
		{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_filter", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_filter", []*pb.QueryViewRow{
		{
			SubjectId: "APT-USDT",
			DataTime:  "2026-06-15T00:00:00+08:00",
			Values: []*pb.ColumnValue{
				testutil.DoubleValue("close", 8.1),
				testutil.DoubleValue("volume", 100),
			},
		},
		{
			SubjectId: "AR-USDT",
			DataTime:  "2026-06-15T00:00:00+08:00",
			Values: []*pb.ColumnValue{
				testutil.DoubleValue("close", 3.2),
				testutil.DoubleValue("volume", 200),
			},
		},
		{
			SubjectId: "OP-USDT",
			DataTime:  "2026-06-15T00:00:00+08:00",
			Values: []*pb.ColumnValue{
				testutil.DoubleValue("close", 10.5),
				testutil.DoubleValue("volume", 50),
			},
		},
	}))

	gotColumns, rows, page, err := store.QueryView(ctx, "view_result_crypto_kline_filter", &pb.QueryViewReq{
		ColumnNames: []string{"close"},
		Filters: []*pb.FilterExpr{{
			Expr: "close >= $min_close",
			Args: map[string]*pb.TypedValue{
				"min_close": {Value: &pb.TypedValue_DoubleValue{DoubleValue: 8}},
			},
		}},
		Sorts: []*pb.SortSpec{{FieldName: "volume", Desc: true}},
	})
	require.NoError(t, err)
	require.Len(t, gotColumns, 1)
	require.Equal(t, "close", gotColumns[0].GetColumnName())
	require.Equal(t, uint64(2), page.GetTotal())
	require.Len(t, rows, 2)
	require.Equal(t, "APT-USDT", rows[0].GetSubjectId())
	require.Equal(t, "OP-USDT", rows[1].GetSubjectId())
	require.Len(t, rows[0].GetValues(), 1)
	require.Equal(t, "close", rows[0].GetValues()[0].GetColumnName())
}

func TestViewStoreRejectsUnsupportedFilterExpression(t *testing.T) {
	ctx := context.Background()
	store, err := duckdb.Open(duckdb.Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	require.NoError(t, err)
	defer store.Close()

	columns := []*pb.ViewColumn{{ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
	require.NoError(t, store.CreateResultTable(ctx, "view_result_crypto_kline_bad_filter", columns))
	require.NoError(t, store.InsertRows(ctx, "view_result_crypto_kline_bad_filter", []*pb.QueryViewRow{{
		SubjectId: "APT-USDT",
		DataTime:  "2026-06-15T00:00:00Z",
		Values:    []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)},
	}}))

	_, _, _, err = store.QueryView(ctx, "view_result_crypto_kline_bad_filter", &pb.QueryViewReq{
		Filters: []*pb.FilterExpr{{Expr: "unsupported(close)"}},
	})
	require.ErrorContains(t, err, "unsupported filter expression")
}
