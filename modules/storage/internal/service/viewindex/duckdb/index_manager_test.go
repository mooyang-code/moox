//go:build cgo

package duckdb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDuckDBPrepareWriteAndPushdownQuery(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
	}}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := func(subject, when string, close float64, volume int64) viewindex.RowWrite {
		return viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, Freq: "1m", DataTime: when}}}}, Fields: []*pb.FieldValue{
			{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: close}}},
			{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: volume}}},
		}}
	}
	batch := viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{
		key("BTC", "2026-07-20T00:00:00Z", 100, 10),
		key("ETH", "2026-07-20T00:01:00Z", 200, 20),
	}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}
	if err := manager.Write(context.Background(), "idx", batch); err != nil {
		t.Fatal(err)
	}
	rows, total, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		Groups: []viewindex.FilterGroup{{Conds: []viewindex.Filter{{Column: "close", Op: pb.FilterOp_FILTER_OP_GTE, Values: []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 150}}}}}}},
		Sorts:  []*pb.SortSpec{{FieldName: "close", Desc: true}}, Limit: 10, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].GetKey().GetSpaceId() != "s" || rows[0].GetKey().GetTimeSeries().GetSubjectId() != "ETH" {
		t.Fatalf("rows=%v total=%d err=%v", rows, total, err)
	}
}

func TestDuckTypeMapping(t *testing.T) {
	cases := map[pb.FieldValueType]string{
		pb.FieldValueType_FIELD_VALUE_TYPE_INT:    "BIGINT",
		pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE: "DOUBLE",
		pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:   "BOOLEAN",
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME:   "TIMESTAMP",
		pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:  "BLOB",
		pb.FieldValueType_FIELD_VALUE_TYPE_JSON:   "JSON",
		pb.FieldValueType_FIELD_VALUE_TYPE_STRING: "VARCHAR",
		pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED: "VARCHAR",
	}
	for valueType, want := range cases {
		if got := duckType(valueType); got != want {
			t.Fatalf("duckType(%s)=%q want %q", valueType, got, want)
		}
	}
}

func TestConditionSQLOperators(t *testing.T) {
	col := "close"
	val := []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}
	sql, args, err := conditionSQL(viewindex.Filter{Column: col, Op: pb.FilterOp_FILTER_OP_GTE, Values: val}, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)
	if err != nil || sql != `"close" >= ?` || len(args) != 1 {
		t.Fatalf("gte: sql=%q args=%v err=%v", sql, args, err)
	}
	sql, args, err = conditionSQL(viewindex.Filter{Column: col, Op: pb.FilterOp_FILTER_OP_IN, Values: []*pb.TypedValue{
		{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}},
		{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}},
	}}, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)
	if err != nil || sql != `"close" IN (?,?)` || len(args) != 2 {
		t.Fatalf("in: sql=%q args=%v err=%v", sql, args, err)
	}
	sql, args, err = conditionSQL(viewindex.Filter{Column: col, Op: pb.FilterOp_FILTER_OP_BETWEEN, Values: []*pb.TypedValue{
		{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}},
		{Value: &pb.TypedValue_DoubleValue{DoubleValue: 2}},
	}}, pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)
	if err != nil || sql != `"close" BETWEEN ? AND ?` || len(args) != 2 {
		t.Fatalf("between: sql=%q args=%v err=%v", sql, args, err)
	}
	sql, args, err = conditionSQL(viewindex.Filter{Column: "name", Op: pb.FilterOp_FILTER_OP_LIKE, Values: []*pb.TypedValue{
		{Value: &pb.TypedValue_StringValue{StringValue: "btc"}},
	}}, pb.FieldValueType_FIELD_VALUE_TYPE_STRING)
	if err != nil || sql != `"name" LIKE '%' || ? || '%'` || len(args) != 1 || args[0] != "btc" {
		t.Fatalf("like substring: sql=%q args=%v err=%v", sql, args, err)
	}
}

func TestFilterSQLGroupLogical(t *testing.T) {
	columns := map[string]pb.FieldValueType{"close": pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "volume": pb.FieldValueType_FIELD_VALUE_TYPE_INT}
	sql, args, err := filterSQL([]viewindex.FilterGroup{
		{Conds: []viewindex.Filter{{Column: "close", Op: pb.FilterOp_FILTER_OP_GT, Values: []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}, Logical: pb.FilterLogical_FILTER_LOGICAL_AND},
		{Conds: []viewindex.Filter{{Column: "volume", Op: pb.FilterOp_FILTER_OP_LT, Values: []*pb.TypedValue{{Value: &pb.TypedValue_IntValue{IntValue: 100}}}}}, Logical: pb.FilterLogical_FILTER_LOGICAL_AND},
	}, pb.FilterLogical_FILTER_LOGICAL_OR, columns)
	if err != nil || !strings.Contains(sql, " OR ") || len(args) != 2 {
		t.Fatalf("group or: sql=%q args=%v err=%v", sql, args, err)
	}
}

func TestDuckDBPrepareDDLAndWriteModes(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
		{ColumnName: "symbol", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	}}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	db, _, _, _, err := manager.getIndex(context.Background(), "idx")
	if err != nil {
		t.Fatal(err)
	}
	var ddl string
	if err := db.QueryRowContext(context.Background(), `SELECT sql FROM duckdb_indexes() WHERE index_name = 'idx_view_rows_data_time'`).Scan(&ddl); err != nil {
		t.Fatalf("data_time index missing: %v", err)
	}
	if !strings.Contains(strings.ToLower(ddl), "data_time") {
		t.Fatalf("unexpected index ddl: %s", ddl)
	}
	var tableSQL string
	if err := db.QueryRowContext(context.Background(), `SELECT sql FROM duckdb_tables() WHERE table_name = 'view_rows'`).Scan(&tableSQL); err != nil {
		t.Fatalf("view_rows table missing: %v", err)
	}
	lower := strings.ToLower(tableSQL)
	if !strings.Contains(lower, "primary key") || !strings.Contains(lower, "subject_id") {
		t.Fatalf("unexpected table ddl: %s", tableSQL)
	}

	key := &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	write := func(mode viewindex.WriteMode, fields []*pb.FieldValue) {
		t.Helper()
		if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
			RowWrites: []viewindex.RowWrite{{Key: viewindex.RowKey{Key: key}, Fields: fields}},
			ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: mode,
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(viewindex.LiveWrite, []*pb.FieldValue{
		{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}},
		{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 10}}},
		{FieldId: "symbol", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "btc-usd"}}},
	})
	// LiveWrite overwrites omitted columns to NULL.
	write(viewindex.LiveWrite, []*pb.FieldValue{
		{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 110}}},
	})
	rows, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{key}, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("live overwrite query: rows=%v err=%v", rows, err)
	}
	var volumePresent bool
	for _, field := range rows[0].GetFields() {
		if field != nil && field.GetFieldId() == "volume" && field.GetValue() != nil {
			volumePresent = true
		}
	}
	if volumePresent {
		t.Fatalf("live write should overwrite omitted columns to null: %v", rows[0].GetFields())
	}
	// Seed again, then Backfill only fills missing.
	write(viewindex.Replace, []*pb.FieldValue{
		{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}},
		{FieldId: "symbol", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "btc"}}},
	})
	write(viewindex.Backfill, []*pb.FieldValue{
		{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 999}}},
		{FieldId: "volume", Value: &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: 42}}},
	})
	rows, _, err = manager.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{key}, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("backfill query: rows=%v err=%v", rows, err)
	}
	var closeVal float64
	var volumeVal int64
	for _, field := range rows[0].GetFields() {
		if field == nil || field.GetValue() == nil {
			continue
		}
		switch field.GetFieldId() {
		case "close":
			closeVal = field.GetValue().GetDoubleValue()
		case "volume":
			volumeVal = field.GetValue().GetIntValue()
		}
	}
	if closeVal != 100 || volumeVal != 42 {
		t.Fatalf("backfill should keep existing close and fill volume: close=%v volume=%v fields=%v", closeVal, volumeVal, rows[0].GetFields())
	}
	rows, _, err = manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		Groups: []viewindex.FilterGroup{{Conds: []viewindex.Filter{{Column: "symbol", Op: pb.FilterOp_FILTER_OP_LIKE, Values: []*pb.TypedValue{{Value: &pb.TypedValue_StringValue{StringValue: "bt"}}}}}}},
		Limit:  10, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("like substring query: rows=%v err=%v", rows, err)
	}
}
