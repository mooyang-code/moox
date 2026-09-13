//go:build cgo

package duckdb

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSeriesWindowReturnsIndependentTopNWithoutGlobalLimit(t *testing.T) {
	ctx := context.Background()
	m, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	require.NoError(t, err)
	defer m.Close()
	require.NoError(t, m.Prepare(ctx, "idx", viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}))
	base := time.Unix(10000, 0).UTC()
	var writes []viewindex.RowWrite
	for subject := 0; subject < 400; subject++ {
		for period := 0; period < 5; period++ {
			at := base.Add(-time.Duration(period*(subject+1)) * time.Minute)
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: fmt.Sprint(subject), Freq: "1m", DataTime: at.Format(time.RFC3339Nano)}}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: float64(period)}}}}})
		}
	}
	for _, partition := range []struct{ freq, tag string }{{"1h", ""}, {"1m", "venue:other"}} {
		for _, original := range writes[:5] {
			copy := original
			copy.Key.Key = proto.Clone(original.Key.Key).(*pb.RowKey)
			copy.Key.Key.GetTimeSeries().Freq = partition.freq
			copy.Key.Key.GetTimeSeries().SeriesTag = partition.tag
			writes = append(writes, copy)
		}
	}
	require.NoError(t, m.Write(ctx, "idx", viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}))
	rows, total, err := m.Query(ctx, "idx", viewindex.QuerySpec{RowsPerSeries: 3, TotalMode: pb.TotalMode_NONE})
	require.NoError(t, err)
	require.EqualValues(t, -1, total)
	require.Len(t, rows, 1206)
	counts := map[string]int{}
	for _, row := range rows {
		key := row.GetKey().GetTimeSeries()
		counts[key.GetSubjectId()+"/"+key.GetFreq()+"/"+key.GetSeriesTag()]++
		require.Less(t, row.GetFields()[0].GetValue().GetDoubleValue(), 3.0)
	}
	require.Len(t, counts, 402)
	for _, count := range counts {
		require.Equal(t, 3, count)
	}
	for _, invalid := range []viewindex.QuerySpec{
		{RowsPerSeries: -1},
		{RowsPerSeries: 3, Limit: 1},
		{RowsPerSeries: 3, Offset: 1},
		{RowsPerSeries: 3, Order: pb.SortOrder_SORT_ORDER_DESC},
		{RowsPerSeries: 3, Sorts: []*pb.SortSpec{{FieldName: "close"}}},
		{RowsPerSeries: 3, AfterKey: &pb.RowKey{}},
		{RowsPerSeries: 3, TotalMode: pb.TotalMode_FORCE_EXACT},
	} {
		_, _, err := m.Query(ctx, "idx", invalid)
		require.Error(t, err)
	}
}

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
	for _, spec := range []viewindex.QuerySpec{{TotalMode: pb.TotalMode_NONE}, {RowsPerSeries: 1, TotalMode: pb.TotalMode_NONE}} {
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
