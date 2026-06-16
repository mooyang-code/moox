package duckdb_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
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
		Values:    []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
	}}))

	gotColumns, rows, page, err := store.QueryView(ctx, "view_result_crypto_kline_1", &pb.QueryViewReq{
		SubjectIds: []string{"APT-USDT"},
		QueryTime:  &pb.QueryTime{TimeRange: &pb.TimeRange{StartTime: "2026-06-15T00:00:00+08:00", EndTime: "2026-06-15T00:00:00+08:00"}},
	})
	require.NoError(t, err)
	require.Len(t, gotColumns, 1)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(1), page.GetTotal())
	require.Equal(t, "APT-USDT", rows[0].GetSubjectId())
}
