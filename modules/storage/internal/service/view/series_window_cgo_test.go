//go:build cgo

package view

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestSeriesWindowRPCRealDuckDBSelectorAndTimeIsolation(t *testing.T) {
	ctx := context.Background()
	m, err := duckdb.OpenIndexManager(duckdb.IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	require.NoError(t, err)
	defer m.Close()
	s, auth := queryTestService(m, true)
	s.engines["duckdb"], s.indexEngine["prices-index"] = m, "duckdb"
	schema := viewindex.ViewIndexSchema{SpaceID: "space", ViewID: "prices", PrimaryDatasetID: "market", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}
	schema.Columns = append(schema.Columns, &pb.ViewColumn{ColumnName: "nullable", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING})
	s.schemas["prices-index"] = schema
	require.NoError(t, m.Prepare(ctx, "prices-index", schema))
	base := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	var writes []viewindex.RowWrite
	for _, partition := range []struct{ subject, freq, tag string }{
		{"BTC", "1m", ""}, {"ETH", "1m", ""}, {"BTC", "1m", "other"}, {"BTC", "1h", ""}, {"OUTSIDE", "1m", ""},
	} {
		for i := 0; i < 5; i++ {
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "space", DatasetId: "market", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: partition.subject, Freq: partition.freq, SeriesTag: partition.tag, DataTime: base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano)}}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(i)}}}}})
		}
	}
	require.NoError(t, m.Write(ctx, "prices-index", viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}))
	tag := ""
	rsp, err := s.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices", RowsPerSeries: 2, TotalMode: pb.TotalMode_NONE, ExpectedActiveIndexId: "prices-index", ExpectedInputContractVersion: "hash:1", TimeRange: &pb.TimeRange{EndTime: base.Add(3 * time.Minute).Format(time.RFC3339Nano)}, ColumnNames: []string{"close"}, Selectors: []*pb.TimeSeriesSelector{{SubjectId: "BTC", Freq: "1m", SeriesTag: &tag}, {SubjectId: "ETH", Freq: "1m", SeriesTag: &tag}, {SubjectId: "MISSING", Freq: "1m", SeriesTag: &tag}}})
	require.NoError(t, err)
	require.Zero(t, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().String())
	require.Equal(t, "hash:1", rsp.GetServedInputContractVersion())
	require.Equal(t, "prices-index", rsp.GetServedActiveIndexId())
	require.False(t, rsp.GetComplete(), "window success does not imply universe readiness")
	require.Len(t, rsp.GetRows(), 4)
	values := map[string][]float64{}
	for _, row := range rsp.GetRows() {
		require.Empty(t, row.GetKey().GetSeriesTag())
		require.Equal(t, "1m", row.GetKey().GetFreq())
		require.Len(t, row.GetFields(), 1)
		values[row.GetKey().GetSubjectId()] = append(values[row.GetKey().GetSubjectId()], row.GetFields()[0].GetValue().GetDoubleValue())
	}
	require.Equal(t, map[string][]float64{"BTC": {1, 2}, "ETH": {1, 2}}, values)
	for _, subject := range []string{"BTC", "MISSING"} {
		full, err := s.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{AuthInfo: auth, SpaceId: "space", ViewId: "prices", RowsPerSeries: 2, TotalMode: pb.TotalMode_NONE, ExpectedActiveIndexId: "prices-index", ExpectedInputContractVersion: "hash:1", TimeRange: &pb.TimeRange{EndTime: base.Add(3 * time.Minute).Format(time.RFC3339Nano)}, Selectors: []*pb.TimeSeriesSelector{{SubjectId: subject, Freq: "1m", SeriesTag: &tag}}})
		require.NoError(t, err)
		require.Zero(t, full.GetRetInfo().GetCode(), full.GetRetInfo().String())
		require.Len(t, full.GetColumns(), 6, "full schema must not depend on returned rows")
		require.Equal(t, "close", full.Columns[4].ColumnName)
		require.Equal(t, "nullable", full.Columns[5].ColumnName)
		require.Equal(t, pb.FieldValueType_FIELD_VALUE_TYPE_STRING, full.Columns[5].ValueType)
		if subject == "MISSING" {
			require.Empty(t, full.GetRows())
			continue
		}
		require.Len(t, full.GetRows(), 2)
		for _, row := range full.GetRows() {
			require.Len(t, row.GetFields(), 2)
			require.Equal(t, "nullable", row.Fields[1].FieldId)
			require.IsType(t, &pb.TypedValue_NullValue{}, row.Fields[1].Value.GetValue())
		}
	}
}
