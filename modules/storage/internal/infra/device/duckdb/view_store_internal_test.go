package duckdb

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestCreateResultTableCreatesTypedColumnsAndIndexes(t *testing.T) {
	ctx := context.Background()
	store := openInternalViewStore(t)
	columns := []*pb.ViewColumn{
		{ColumnName: "kline.close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "kline.volume", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "kline.status", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.status", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	}
	require.NoError(t, store.CreateResultTable(ctx, "ts_view_crypto_kline_schema", columns))

	gotColumns := tableColumns(t, store, "ts_view_crypto_kline_schema")
	require.Contains(t, gotColumns, "row_key")
	require.Contains(t, gotColumns, "subject_id")
	require.Contains(t, gotColumns, "freq")
	require.Contains(t, gotColumns, "data_time")
	require.Contains(t, gotColumns, "kline.close")
	require.Contains(t, gotColumns, "kline.volume")
	require.Contains(t, gotColumns, "kline.status")

	gotIndexes := tableIndexes(t, store, "ts_view_crypto_kline_schema")
	require.Contains(t, gotIndexes, "idx_ts_view_crypto_kline_schema_key_time")
	require.Contains(t, gotIndexes, "idx_ts_view_crypto_kline_schema_subject_freq_time")
	require.Contains(t, gotIndexes, "idx_ts_view_crypto_kline_schema_kline_close")
	require.Contains(t, gotIndexes, "idx_ts_view_crypto_kline_schema_kline_volume")
	require.Contains(t, gotIndexes, "idx_ts_view_crypto_kline_schema_kline_status")
}

func TestQueryTimeSeriesRowsUsesTypedColumnsForFiltersSortsAndPagination(t *testing.T) {
	ctx := context.Background()
	store := openInternalViewStore(t)
	columns := []*pb.ViewColumn{
		{ColumnName: "kline.close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "kline.volume", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	require.NoError(t, store.CreateResultTable(ctx, "ts_view_crypto_kline_query", columns))
	require.NoError(t, store.InsertRows(ctx, "ts_view_crypto_kline_query", []*pb.TimeSeriesRow{
		{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-06-15T00:00:00Z"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("kline.close", 100), testutil.DoubleValue("kline.volume", 10)}},
		{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-06-15T01:00:00Z"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("kline.close", 120), testutil.DoubleValue("kline.volume", 20)}},
		{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-06-15T02:00:00Z"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("kline.close", 110), testutil.DoubleValue("kline.volume", 30)}},
		{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "5m", DataTime: "2026-06-15T01:00:00Z"}, Columns: []*pb.ColumnValue{testutil.DoubleValue("kline.close", 999), testutil.DoubleValue("kline.volume", 99)}},
	}))

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "ts_view_crypto_kline_query", &pb.QueryTimeSeriesRowsReq{
		Keys:      []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT", Freq: "1h"}},
		TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:30:00Z", EndTime: "2026-06-15T03:00:00Z"},
		Filters: []*pb.FilterExpr{{
			Expr: "kline.close >= $min_close",
			Args: map[string]*pb.TypedValue{"min_close": {Value: &pb.TypedValue_DoubleValue{DoubleValue: 105}}},
		}},
		Sorts: []*pb.SortSpec{{FieldName: "kline.volume", Desc: true}},
		Page:  &pb.Page{Page: 1, Size: 1},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(2), page.GetTotal())
	require.True(t, page.GetHasMore())
	require.Len(t, rows, 1)
	require.Equal(t, "2026-06-15T02:00:00.000000000Z", rows[0].GetKey().GetDataTime())
	require.InDelta(t, 110, rows[0].GetColumns()[0].GetValue().GetDoubleValue(), 1e-9)
}

func TestInsertRowsSerializesConcurrentInitialIndexRebuildsPerTable(t *testing.T) {
	ctx := context.Background()
	store := openInternalViewStore(t)
	columns := []*pb.ViewColumn{
		{ColumnName: "kline.close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "kline.volume", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	require.NoError(t, store.CreateResultTable(ctx, "ts_view_crypto_kline_concurrent", columns))

	start := make(chan struct{})
	errs := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < cap(errs); i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.InsertRows(ctx, "ts_view_crypto_kline_concurrent", []*pb.TimeSeriesRow{
				{
					Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-06-15T00:00:00Z"},
					Columns: []*pb.ColumnValue{
						testutil.DoubleValue("kline.close", float64(100+i)),
						testutil.DoubleValue("kline.volume", float64(i)),
					},
				},
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "ts_view_crypto_kline_concurrent", &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT", Freq: "1h"}},
	})
	require.NoError(t, err)
	require.Equal(t, uint32(1), page.GetTotal())
	require.Len(t, rows, 1)
}

func openInternalViewStore(t *testing.T) *ViewStore {
	t.Helper()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func tableColumns(t *testing.T, store *ViewStore, tableName string) map[string]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT column_name, data_type FROM information_schema.columns WHERE table_name = ?`, tableName)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name string
		var typ string
		require.NoError(t, rows.Scan(&name, &typ))
		out[name] = typ
	}
	require.NoError(t, rows.Err())
	return out
}

func tableIndexes(t *testing.T, store *ViewStore, tableName string) map[string]bool {
	t.Helper()
	rows, err := store.db.Query(`SELECT index_name FROM duckdb_indexes() WHERE table_name = ?`, tableName)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		out[name] = true
	}
	require.NoError(t, rows.Err())
	return out
}
