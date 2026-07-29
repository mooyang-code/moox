//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func duckRowKey(space, dataset, subject, freq, at, tag string) *pb.RowKey {
	return &pb.RowKey{
		SpaceId: space, DatasetId: dataset,
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: subject, Freq: freq, DataTime: at, SeriesTag: tag,
		}},
	}
}

func stringPtr(value string) *string { return &value }

func rowTags(rows []*pb.RowFieldValues) []string {
	tags := make([]string, 0, len(rows))
	for _, row := range rows {
		tags = append(tags, row.GetKey().GetTimeSeries().GetSeriesTag())
	}
	return tags
}

func fieldDouble(row *pb.RowFieldValues, fieldID string) float64 {
	for _, field := range row.GetFields() {
		if field.GetFieldId() == fieldID {
			return field.GetValue().GetDoubleValue()
		}
	}
	return 0
}

func TestDuckDBSeriesTagSchemaIdentitySelectorsAndUpsert(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1,
		Engine: "duckdb", SchemaHash: "hash",
		Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	db, _, _, _, err := manager.getIndex(context.Background(), "idx")
	if err != nil {
		t.Fatal(err)
	}
	var tableSQL string
	if err := db.QueryRowContext(context.Background(), `SELECT sql FROM duckdb_tables() WHERE table_name = 'view_rows'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(tableSQL)), " ")
	if !strings.Contains(normalized, "series_tag varchar") {
		t.Fatalf("series_tag is not a scalar system column: %s", tableSQL)
	}
	var seriesTagNotNull bool
	infoRows, err := db.QueryContext(context.Background(), `PRAGMA table_info('view_rows')`)
	if err != nil {
		t.Fatal(err)
	}
	for infoRows.Next() {
		var cid int
		var notNull, primary bool
		var name, columnType string
		var defaultValue any
		if err := infoRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		if name == "series_tag" {
			seriesTagNotNull = notNull
		}
	}
	if err := infoRows.Close(); err != nil {
		t.Fatal(err)
	}
	if !seriesTagNotNull {
		t.Fatalf("series_tag is nullable: %s", tableSQL)
	}
	if !strings.Contains(normalized, "primary key(subject_id, freq, data_time, series_tag)") &&
		!strings.Contains(normalized, "primary key (subject_id, freq, data_time, series_tag)") {
		t.Fatalf("unexpected primary key: %s", tableSQL)
	}
	at := "2026-07-29T00:00:00Z"
	write := func(tag string, close float64) viewindex.RowWrite {
		return viewindex.RowWrite{
			Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC-USDT", "1m", at, tag)},
			Fields: []*pb.FieldValue{{
				FieldId: "close",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: close}},
			}},
		}
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites:      []viewindex.RowWrite{write("", 1), write("venue:binance", 2), write("venue:okx", 3)},
		ViewRevision:   1,
		ViewSchemaHash: "hash",
		WriteMode:      viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites:      []viewindex.RowWrite{write("venue:okx", 30)},
		ViewRevision:   1,
		ViewSchemaHash: "hash",
		WriteMode:      viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		tag  *string
		want []string
	}{
		{name: "absent", want: []string{"", "venue:binance", "venue:okx"}},
		{name: "present empty", tag: stringPtr(""), want: []string{""}},
		{name: "present value", tag: stringPtr("venue:okx"), want: []string{"venue:okx"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
				Selectors: []viewindex.TimeSeriesSelector{{
					SpaceID: "s", DatasetID: "prices", SubjectID: "BTC-USDT", Freq: "1m", SeriesTag: tc.tag,
				}},
				Limit: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := rowTags(rows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tags=%v want=%v", got, tc.want)
			}
			if tc.name == "absent" {
				wantClose := map[string]float64{"": 1, "venue:binance": 2, "venue:okx": 30}
				for _, row := range rows {
					tag := row.GetKey().GetTimeSeries().GetSeriesTag()
					if got := fieldDouble(row, "close"); got != wantClose[tag] {
						t.Fatalf("%q close=%v want=%v", tag, got, wantClose[tag])
					}
				}
			}
		})
	}
	rows, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		Keys:  []*pb.RowKey{duckRowKey("s", "prices", "BTC-USDT", "1m", at, "venue:okx")},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowTags(rows); !reflect.DeepEqual(got, []string{"venue:okx"}) {
		t.Fatalf("exact key tags=%v", got)
	}
}

func TestDuckDBStableTotalOrderAndPagination(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1,
		Engine: "duckdb", SchemaHash: "hash",
		Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	at := "2026-07-29T00:00:00Z"
	writes := []viewindex.RowWrite{
		{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", at, "venue:binance")}},
		{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", at, "venue:okx")}},
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: writes, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}

	queryPages := func(order pb.SortOrder) []string {
		t.Helper()
		var tags []string
		for offset := 0; offset < 2; offset++ {
			rows, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
				Order: order, Limit: 1, Offset: offset,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("offset %d returned %d rows", offset, len(rows))
			}
			tags = append(tags, rows[0].GetKey().GetTimeSeries().GetSeriesTag())
		}
		return tags
	}
	asc := queryPages(pb.SortOrder_SORT_ORDER_ASC)
	desc := queryPages(pb.SortOrder_SORT_ORDER_DESC)
	if !reflect.DeepEqual(asc, []string{"venue:binance", "venue:okx"}) {
		t.Fatalf("asc=%v", asc)
	}
	if !reflect.DeepEqual(desc, []string{"venue:okx", "venue:binance"}) {
		t.Fatalf("desc=%v", desc)
	}

	if got := orderSQL(nil, pb.SortOrder_SORT_ORDER_ASC, map[string]pb.FieldValueType{}); got !=
		` ORDER BY "subject_id" ASC, "freq" ASC, "data_time" ASC, "series_tag" ASC` {
		t.Fatalf("default order=%q", got)
	}
	if got := orderSQL(nil, pb.SortOrder_SORT_ORDER_DESC, map[string]pb.FieldValueType{}); got !=
		` ORDER BY "subject_id" DESC, "freq" DESC, "data_time" DESC, "series_tag" DESC` {
		t.Fatalf("desc order=%q", got)
	}
	if got := orderSQL([]*pb.SortSpec{{FieldName: "close", Desc: true}, {FieldName: "data_time", Desc: true}}, pb.SortOrder_SORT_ORDER_ASC, map[string]pb.FieldValueType{"close": pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}); got !=
		` ORDER BY "close" DESC, "data_time" DESC, "subject_id" ASC, "freq" ASC, "series_tag" ASC` {
		t.Fatalf("custom order=%q", got)
	}
}

func TestDuckDBAfterCursorIncludesSeriesTag(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1,
		Engine: "duckdb", SchemaHash: "hash",
	}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	at := "2026-07-29T00:00:00Z"
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{
			{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", at, "a")}},
			{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", at, "b")}},
		},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	first, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{Limit: 1})
	if err != nil || len(first) != 1 {
		t.Fatalf("first=%v err=%v", first, err)
	}
	second, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		AfterKey: first[0].GetKey(), Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rowTags(second); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("cursor skipped same-timestamp tag: %v", got)
	}
}

func TestDuckDBExactKeyRequiresDataTimeAndAllowsEmptySeriesTag(t *testing.T) {
	columns := map[string]pb.FieldValueType{}
	_, _, err := buildWhere(viewindex.QuerySpec{Keys: []*pb.RowKey{
		duckRowKey("s", "prices", "BTC", "1m", "", ""),
	}}, columns)
	if err == nil || !strings.Contains(err.Error(), "data_time") {
		t.Fatalf("empty data_time error=%v", err)
	}

	where, args, err := buildWhere(viewindex.QuerySpec{Keys: []*pb.RowKey{
		duckRowKey("s", "prices", "BTC", "1m", "2026-07-29T00:00:00Z", ""),
	}}, columns)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(where.sql, "data_time = ? AND series_tag = ?") {
		t.Fatalf("exact default-tag where=%q", where.sql)
	}
	if got := args[len(args)-1]; got != "" {
		t.Fatalf("default series_tag arg=%v", got)
	}
}

func TestDuckDBRejectsMalformedSystemSchema(t *testing.T) {
	cases := []struct {
		name string
		ddl  string
	}{
		{
			name: "missing series tag",
			ddl:  `CREATE TABLE view_rows (subject_id VARCHAR NOT NULL, freq VARCHAR NOT NULL, data_time TIMESTAMP_NS NOT NULL, PRIMARY KEY(subject_id, freq, data_time))`,
		},
		{
			name: "wrong primary key",
			ddl:  `CREATE TABLE view_rows (subject_id VARCHAR NOT NULL, freq VARCHAR NOT NULL, data_time TIMESTAMP_NS NOT NULL, series_tag VARCHAR NOT NULL, PRIMARY KEY(subject_id, freq, data_time))`,
		},
		{
			name: "wrong series tag type",
			ddl:  `CREATE TABLE view_rows (subject_id VARCHAR NOT NULL, freq VARCHAR NOT NULL, data_time TIMESTAMP_NS NOT NULL, series_tag BIGINT NOT NULL, PRIMARY KEY(subject_id, freq, data_time, series_tag))`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "duckdb")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			path := filepath.Join(root, "idx.duckdb")
			db, err := sql.Open("duckdb", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec(`
				CREATE TABLE view_meta (singleton INTEGER PRIMARY KEY, view_version UBIGINT NOT NULL, schema_hash VARCHAR NOT NULL, primary_dataset_id VARCHAR NOT NULL, space_id VARCHAR NOT NULL, updated_at VARCHAR NOT NULL);
				INSERT INTO view_meta VALUES (1, 1, 'hash', 'prices', 's', 'now');
				CREATE TABLE view_columns (column_name VARCHAR PRIMARY KEY, value_type INTEGER NOT NULL);
				` + tc.ddl)
			closeErr := db.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			_, err = manager.Stat(context.Background(), "idx")
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "clean") || !strings.Contains(strings.ToLower(err.Error()), "rebuild") {
				t.Fatalf("Stat error=%v, want cleanup/rebuild instruction", err)
			}
		})
	}
}

func TestOpenIndexManagerAcceptsCurrentSchema(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), "current", viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices",
		ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("reopen current schema: %v", err)
	}
	defer reopened.Close()
	stats, err := reopened.Stat(context.Background(), "current")
	if err != nil || !stats.Exists {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

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

func TestDuckDBQualifiedViewColumn(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	const column = "prices.close"
	schema := viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices",
		ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash",
		Columns: []*pb.ViewColumn{{ColumnName: column, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{
		SpaceId: "s", DatasetId: "prices",
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: "BTC-USDT", Freq: "1h", DataTime: "2026-07-20T00:00:00Z",
		}},
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{
			Key: viewindex.RowKey{Key: key},
			Fields: []*pb.FieldValue{{
				FieldId: column,
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 123.5}},
			}},
		}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		Groups: []viewindex.FilterGroup{{Conds: []viewindex.Filter{{
			Column: column,
			Op:     pb.FilterOp_FILTER_OP_GTE,
			Values: []*pb.TypedValue{{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}},
		}}}},
		Sorts:    []*pb.SortSpec{{FieldName: column, Desc: true}},
		Includes: []string{column},
		Limit:    1, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("qualified query: rows=%v total=%d err=%v", rows, total, err)
	}
	fields := rows[0].GetFields()
	if len(fields) != 1 || fields[0].GetFieldId() != column || fields[0].GetValue().GetDoubleValue() != 123.5 {
		t.Fatalf("qualified field not retained: %v", fields)
	}
}

func TestDuckDBNanosecondDataTimeIdentityAndRange(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices",
		ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash",
		Columns: []*pb.ViewColumn{{ColumnName: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Nanosecond)
	write := func(at time.Time, value float64) viewindex.RowWrite {
		return viewindex.RowWrite{
			Key: viewindex.RowKey{Key: &pb.RowKey{
				SpaceId: "s", DatasetId: "prices",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC", Freq: "1m", DataTime: at.Format(time.RFC3339Nano),
				}},
			}},
			Fields: []*pb.FieldValue{{
				FieldId: "value",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
			}},
		}
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites:      []viewindex.RowWrite{write(first, 1), write(second, 2)},
		ViewRevision:   1,
		ViewSchemaHash: "hash",
		WriteMode:      viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, total, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		TimeRange: &pb.TimeRange{
			StartTime: first.Format(time.RFC3339Nano),
			EndTime:   second.Add(time.Nanosecond).Format(time.RFC3339Nano),
		},
		Sorts:     []*pb.SortSpec{{FieldName: "data_time"}},
		Limit:     10,
		TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("nanosecond-distinct rows collapsed: total=%d rows=%v", total, rows)
	}
	if got := rows[0].GetKey().GetTimeSeries().GetDataTime(); got != first.Format(time.RFC3339Nano) {
		t.Fatalf("first data_time=%q", got)
	}
	if got := rows[1].GetKey().GetTimeSeries().GetDataTime(); got != second.Format(time.RFC3339Nano) {
		t.Fatalf("second data_time=%q", got)
	}
	rows, total, err = manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		TimeRange: &pb.TimeRange{
			StartTime: first.Format(time.RFC3339Nano),
			EndTime:   second.Format(time.RFC3339Nano),
		},
		Limit:     10,
		TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].GetKey().GetTimeSeries().GetDataTime() != first.Format(time.RFC3339Nano) {
		t.Fatalf("half-open nanosecond range mismatch: total=%d rows=%v", total, rows)
	}
}

func TestDuckTypeMapping(t *testing.T) {
	cases := map[pb.FieldValueType]string{
		pb.FieldValueType_FIELD_VALUE_TYPE_INT:         "BIGINT",
		pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:      "DOUBLE",
		pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:        "BOOLEAN",
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME:        "TIMESTAMP_NS",
		pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:       "BLOB",
		pb.FieldValueType_FIELD_VALUE_TYPE_JSON:        "JSON",
		pb.FieldValueType_FIELD_VALUE_TYPE_STRING:      "VARCHAR",
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
			RowWrites:    []viewindex.RowWrite{{Key: viewindex.RowKey{Key: key}, Fields: fields}},
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
	write(viewindex.LiveWrite, []*pb.FieldValue{
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
