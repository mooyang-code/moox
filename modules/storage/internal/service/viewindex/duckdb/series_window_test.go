//go:build cgo

package duckdb

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestQueryJSONPreservesTextAndDistinguishesJSONNull(t *testing.T) {
	ctx := context.Background()
	m, err := OpenIndexManager(IndexManagerOptions{Root: t.TempDir()})
	require.NoError(t, err)
	defer m.Close()
	require.NoError(t, m.Prepare(ctx, "idx", viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "payload", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON}}}))
	const payload = `{"large":18446744073709551615,"decimal":0.12345678901234567890123456789}`
	var writes []viewindex.RowWrite
	for subject, raw := range map[string]string{"BTC": payload, "ETH": "null"} {
		writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: "2026-09-13T00:00:00Z"}}}}, Fields: []*pb.FieldValue{{FieldId: "payload", Value: &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: raw}}}}})
	}
	require.NoError(t, m.Write(ctx, "idx", viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}))
	for _, spec := range []viewindex.QuerySpec{{TotalMode: pb.TotalMode_NONE}} {
		rows, _, err := m.Query(ctx, "idx", spec)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		for _, row := range rows {
			require.Len(t, row.Fields, 1)
			want := "null"
			if row.Key.GetTimeSeries().SubjectId == "BTC" {
				want = payload
			}
			require.Equal(t, want, row.Fields[0].Value.GetStringValue())
		}
	}
}
