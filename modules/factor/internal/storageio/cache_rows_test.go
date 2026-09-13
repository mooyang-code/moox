package storageio

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func cacheTestColumns() []*pb.ResultColumn {
	return []*pb.ResultColumn{
		{ColumnName: "subject_id", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "freq", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "data_time", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		{ColumnName: "series_tag", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{ColumnName: "count", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
	}
}

func TestDecodeCacheRowsPreservesSchemaEmptyAndExactValues(t *testing.T) {
	columns := cacheTestColumns()
	schema, rows, err := decodeCacheRows(columns, nil)
	require.NoError(t, err)
	require.Len(t, schema, 5)
	require.Empty(t, rows)
	require.Equal(t, "TIMESTAMP_NS", schema[2].Type)
	row := &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SubjectId: "BTC", Freq: "1m", DataTime: "2026-09-12T00:00:00.123456789Z"}, Fields: []*pb.FieldValue{
		{FieldId: "count", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 9223372036854775807}}},
	}}
	_, rows, err = decodeCacheRows(columns, []*pb.TimeSeriesRow{row})
	require.NoError(t, err)
	require.Equal(t, int64(9223372036854775807), rows[0][4])
	require.Equal(t, 123456789, rows[0][2].(time.Time).Nanosecond())
	db, err := inputcache.CreateDatabase(context.Background(), filepath.Join(t.TempDir(), "decoded.duckdb"), schema, []string{"subject_id", "freq", "data_time", "series_tag"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Upsert(context.Background(), rows, time.Now()))
	cached, err := db.ReadWindow(context.Background(), inputcache.WindowQuery{TimeColumn: "data_time", Through: rows[0][2].(time.Time), Lookback: 1})
	require.NoError(t, err)
	require.Equal(t, rows, cached)
	row.Fields[0].Value = &pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}}
	_, rows, err = decodeCacheRows(columns, []*pb.TimeSeriesRow{row})
	require.NoError(t, err)
	require.Nil(t, rows[0][4])
}

func TestDecodeCacheRowsRejectsPartialOrAmbiguousInput(t *testing.T) {
	row := func(fields ...*pb.FieldValue) *pb.TimeSeriesRow {
		return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SubjectId: "BTC", Freq: "1m", DataTime: "2026-09-12T00:00:00Z"}, Fields: fields}
	}
	f := &pb.FieldValue{FieldId: "count", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 1}}}
	for _, input := range [][]*pb.TimeSeriesRow{
		{nil}, {row()}, {row(f, f)}, {row(&pb.FieldValue{FieldId: "unknown", Value: f.Value})},
		{row(&pb.FieldValue{FieldId: "count"})},
		{row(&pb.FieldValue{FieldId: "count", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "1"}}})},
		{row(f), row(f)},
	} {
		_, _, err := decodeCacheRows(cacheTestColumns(), input)
		require.Error(t, err)
	}
	for _, columns := range [][]*pb.ResultColumn{nil, cacheTestColumns()[1:], append(cacheTestColumns(), cacheTestColumns()[0]), append(cacheTestColumns(), &pb.ResultColumn{ColumnName: "bad"})} {
		_, _, err := decodeCacheRows(columns, nil)
		require.Error(t, err)
	}
}

func TestCacheFieldValueTypesAndNullAreStrict(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		value *pb.TypedValue
		want  any
	}{
		{"VARCHAR", &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: ""}}, ""},
		{"DOUBLE", &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.5}}, 1.5},
		{"BOOLEAN", &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: false}}, false},
		{"JSON", &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: `{"n":9223372036854775807}`}}, `{"n":9223372036854775807}`},
		{"JSON", &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: `null`}}, `null`},
		{"BLOB", &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: nil}}, []byte{}},
	} {
		value, err := cacheFieldValue(tc.kind, tc.value)
		require.NoError(t, err)
		require.Equal(t, tc.want, value)
	}
	for _, value := range []*pb.TypedValue{nil, {}, {Value: &pb.TypedValue_JsonValue{JsonValue: "{"}}, {Value: &pb.TypedValue_NullValue{}}} {
		_, err := cacheFieldValue("JSON", value)
		require.Error(t, err)
	}
	_, err := cacheTime("9999-01-01T00:00:00Z")
	require.Error(t, err)
	bytes := []byte{1, 2}
	value, err := cacheFieldValue("BLOB", &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: bytes}})
	require.NoError(t, err)
	bytes[0] = 9
	require.Equal(t, []byte{1, 2}, value)
}
