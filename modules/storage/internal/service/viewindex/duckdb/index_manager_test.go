//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestDuckDBContextDetachesCancellationAfterStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	detached, err := duckDBContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-detached.Done():
		t.Fatal("DuckDB context must not be cancelled after an operation starts")
	default:
	}
}

func TestDuckDBContextRejectsAlreadyCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := duckDBContext(ctx); err != context.Canceled {
		t.Fatalf("duckDBContext error = %v, want context.Canceled", err)
	}
}

func TestOpenAppliesResourceLimits(t *testing.T) {
	t.Setenv(duckDBMemoryLimitEnv, "128MB")
	t.Setenv(duckDBThreadsEnv, "1")

	db, err := open(filepath.Join(t.TempDir(), "view.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var memoryLimit string
	if err := db.QueryRow(`SELECT current_setting('memory_limit')`).Scan(&memoryLimit); err != nil {
		t.Fatal(err)
	}
	var memoryMiB float64
	var memoryUnit string
	if _, err := fmt.Sscanf(memoryLimit, "%f %s", &memoryMiB, &memoryUnit); err != nil {
		t.Fatalf("memory_limit=%q is not a DuckDB memory value: %v", memoryLimit, err)
	}
	if memoryMiB < 100 || memoryMiB > 130 || !strings.EqualFold(memoryUnit, "MiB") {
		t.Fatalf("memory_limit=%q, want approximately 128MB", memoryLimit)
	}
	var threads int
	if err := db.QueryRow(`SELECT current_setting('threads')`).Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if threads != 1 {
		t.Fatalf("threads=%d, want 1", threads)
	}
}

func TestOpenUsesDefaultMemoryLimit(t *testing.T) {
	t.Setenv(duckDBMemoryLimitEnv, "")
	db, err := open(filepath.Join(t.TempDir(), "view.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Stats().MaxOpenConnections; got != defaultMaxOpenConns {
		t.Fatalf("max open connections=%d, want %d", got, defaultMaxOpenConns)
	}
	if _, err := db.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("prime connection pool: %v", err)
	}
	if got := db.Stats().Idle; got > defaultMaxIdleConns {
		t.Fatalf("idle connections=%d, want <= %d", got, defaultMaxIdleConns)
	}

	var memoryLimit string
	if err := db.QueryRow(`SELECT current_setting('memory_limit')`).Scan(&memoryLimit); err != nil {
		t.Fatal(err)
	}
	var memoryMiB float64
	var memoryUnit string
	if _, err := fmt.Sscanf(memoryLimit, "%f %s", &memoryMiB, &memoryUnit); err != nil {
		t.Fatalf("memory_limit=%q is not a DuckDB memory value: %v", memoryLimit, err)
	}
	if memoryMiB < 230 || memoryMiB > 260 || !strings.EqualFold(memoryUnit, "MiB") {
		t.Fatalf("memory_limit=%q, want approximately 256MB", memoryLimit)
	}
}

func TestConcurrentColdOpenDoesNotLeaveManagerLocked(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}
	first, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, _, err := manager.getIndex(context.Background(), "idx")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

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

func TestResolveIncludedColumnsMapsUniqueLogicalSuffix(t *testing.T) {
	columns := map[string]pb.FieldValueType{
		"prices.close":  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		"prices.volume": pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	}
	got, err := resolveIncludedColumns(columns, []string{"close"})
	if err != nil {
		t.Fatalf("resolve unique suffix: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"prices.close"}) {
		t.Fatalf("resolve unique suffix = %v, want [prices.close]", got)
	}

	columns["adjusted.close"] = pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	_, err = resolveIncludedColumns(columns, []string{"close"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("resolve ambiguous suffix error = %v", err)
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
			name: "legacy dimensions schema",
			ddl:  `CREATE TABLE view_rows (subject_id VARCHAR NOT NULL, freq VARCHAR NOT NULL, data_time TIMESTAMP_NS NOT NULL, dimensions_json VARCHAR NOT NULL, PRIMARY KEY(subject_id, freq, data_time, dimensions_json))`,
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
	if stats.PhysicalBytes == 0 {
		t.Fatalf("physical bytes=%d, want non-zero", stats.PhysicalBytes)
	}
}

func TestOpenIndexManagerDefersExistingIndexValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "inactive.duckdb")
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE not_a_view (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("incompatible inactive index blocked startup: %v", err)
	}
	defer manager.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("existing index should remain for metadata recovery, stat err=%v", err)
	}
}

func TestOpenIndexManagerRemovesInterruptedPrepareFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tempPath := filepath.Join(root, "slot.duckdb.prepare-123")
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenIndexManager(IndexManagerOptions{Root: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("interrupted prepare file remains, stat err=%v", err)
	}
}

func TestRemoveDeletesDatabaseAndWAL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), "retired", viewindex.ViewIndexSchema{
		SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash",
	}); err != nil {
		t.Fatal(err)
	}
	path, err := manager.path("retired")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".wal", []byte("stale wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), "retired"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, path + ".wal"} {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Fatalf("retired index artifact %q still exists, err=%v", file, err)
		}
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

func TestStatMetadataTracksCoverageWithoutScanningRows(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash"}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	row := func(at string) viewindex.RowWrite {
		return viewindex.RowWrite{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", at, "")}}
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{
			row("2026-07-20T00:01:00Z"),
			row("2026-07-20T00:00:00Z"),
		}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	stats, err := manager.StatMetadata(context.Background(), "idx")
	if err != nil {
		t.Fatal(err)
	}
	if stats.IndexedFrom != "2026-07-20T00:00:00.000000000Z" || stats.IndexedTo != "2026-07-20T00:01:00.000000000Z" {
		t.Fatalf("coverage=%s..%s", stats.IndexedFrom, stats.IndexedTo)
	}
}

func TestDuckDBSeriesCapacityTracksEachSeriesIndependently(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash"}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	row := func(subject, tag string, minute int) viewindex.RowWrite {
		return viewindex.RowWrite{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", subject, "1m", fmt.Sprintf("2026-07-20T00:%02d:00Z", minute), tag)}}
	}
	rows := []viewindex.RowWrite{row("BTC", "venue:binance", 0), row("BTC", "venue:binance", 1), row("BTC", "venue:binance", 2), row("BTC", "venue:other", 0), row("ETH", "venue:binance", 0)}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: rows, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
	// A field patch for an existing row must not increase the physical count.
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: []viewindex.RowWrite{row("BTC", "venue:binance", 1)}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
	if got, err := manager.SeriesCapacity(context.Background(), "idx", 3); err != nil || got.Exceeded {
		t.Fatalf("capacity at exact limit = %#v, err=%v", got, err)
	}
	got, err := manager.SeriesCapacity(context.Background(), "idx", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exceeded || got.SubjectID != "BTC" || got.Freq != "1m" || got.SeriesTag != "venue:binance" || got.Rows != 3 {
		db, _, _, _, dbErr := manager.getIndex(context.Background(), "idx")
		if dbErr == nil {
			rows, queryErr := db.Query(`SELECT subject_id, freq, series_tag, row_count FROM view_series_counts ORDER BY subject_id, series_tag`)
			if queryErr == nil {
				defer rows.Close()
				for rows.Next() {
					var subject, freq, tag string
					var count uint64
					_ = rows.Scan(&subject, &freq, &tag, &count)
					t.Logf("count row subject=%s freq=%s tag=%s count=%d", subject, freq, tag, count)
				}
			}
		}
		t.Fatalf("unexpected capacity offender: %#v", got)
	}
	if got, err := manager.SeriesCapacity(context.Background(), "idx", 99); err != nil || got.Exceeded {
		t.Fatalf("below-capacity result = %#v, err=%v", got, err)
	}
}

func TestDuckDBSeriesCapacityUsesCurrentPhysicalRows(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash"}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	row := func(minute int) viewindex.RowWrite {
		return viewindex.RowWrite{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", fmt.Sprintf("2026-07-20T00:%02d:00Z", minute), "venue:binance")}}
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{row(0), row(1), row(2)}, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	db, _, _, _, err := manager.getIndex(context.Background(), "idx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `DELETE FROM view_rows WHERE subject_id = 'BTC' AND freq = '1m' AND data_time = TIMESTAMP '2026-07-20 00:00:00' AND series_tag = 'venue:binance'`); err != nil {
		t.Fatal(err)
	}
	got, err := manager.SeriesCapacity(context.Background(), "idx", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Exceeded || got.Rows != 0 {
		t.Fatalf("capacity should use current physical rows after deletion: %#v", got)
	}
	var repaired uint64
	if err := db.QueryRowContext(context.Background(), `SELECT row_count FROM view_series_counts WHERE subject_id = 'BTC' AND freq = '1m' AND series_tag = 'venue:binance'`).Scan(&repaired); err != nil {
		t.Fatal(err)
	}
	if repaired != 2 {
		t.Fatalf("stale capacity counter was not repaired: got %d, want 2", repaired)
	}
}

func TestDuckDBSeriesCapacityChecksAllStaleCandidates(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash"}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	row := func(subject string, minute int) viewindex.RowWrite {
		hour, min := minute/60, minute%60
		return viewindex.RowWrite{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", subject, "1m", fmt.Sprintf("2026-07-20T%02d:%02d:00Z", hour, min), "venue:binance")}}
	}
	writes := make([]viewindex.RowWrite, 0, 66)
	for i := 0; i < 65; i++ {
		writes = append(writes, row(fmt.Sprintf("A%02d", i), 0))
	}
	writes = append(writes, row("ZZZ", 0), row("ZZZ", 1), row("ZZZ", 2))
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: writes, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
	db, _, _, _, err := manager.getIndex(context.Background(), "idx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE view_series_counts SET row_count = 3 WHERE subject_id <> 'ZZZ'`); err != nil {
		t.Fatal(err)
	}
	got, err := manager.SeriesCapacity(context.Background(), "idx", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exceeded || got.SubjectID != "ZZZ" || got.Rows != 3 {
		t.Fatalf("capacity should find the offender after stale candidates: %#v", got)
	}
}

func TestMergeCoverageBoundsIsMonotonicAndCanonical(t *testing.T) {
	from, to := mergeCoverageBounds(
		"2026-08-12T00:00:00Z", "2026-08-12T01:00:00.1Z",
		"2026-08-12T00:30:00.123456789Z", "2026-08-12T00:45:00Z",
	)
	if from != "2026-08-12T00:00:00.000000000Z" || to != "2026-08-12T01:00:00.100000000Z" {
		t.Fatalf("bounds moved backwards or were not canonical: %s..%s", from, to)
	}
	from, to = mergeCoverageBounds(
		"", "", "2026-08-12T00:00:00.1+00:00", "2026-08-12T00:00:00.9+00:00",
	)
	if from != "2026-08-12T00:00:00.100000000Z" || to != "2026-08-12T00:00:00.900000000Z" {
		t.Fatalf("bounds were not normalized: %s..%s", from, to)
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
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{{
			Key: viewindex.RowKey{Key: key},
			Fields: []*pb.FieldValue{{FieldId: column, Value: &pb.TypedValue{
				Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL},
			}}},
		}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, err = manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		Includes: []string{column}, Limit: 1, TotalMode: pb.TotalMode_NONE,
	})
	if err != nil || len(rows) != 1 || len(rows[0].GetFields()) != 1 ||
		rows[0].GetFields()[0].GetFieldId() != column ||
		rows[0].GetFields()[0].GetValue().GetNullValue() != pb.NullValue_NULL_VALUE_NULL {
		t.Fatalf("qualified NULL field not retained: rows=%v err=%v", rows, err)
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
	// LiveWrite updates only the fields present in the patch; omitted columns
	// remain available for the next factor read.
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
	if !volumePresent {
		t.Fatalf("live write should preserve omitted columns: %v", rows[0].GetFields())
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
	if closeVal != 100 || volumeVal != 10 {
		t.Fatalf("backfill should keep existing close and volume: close=%v volume=%v fields=%v", closeVal, volumeVal, rows[0].GetFields())
	}
	rows, _, err = manager.Query(context.Background(), "idx", viewindex.QuerySpec{
		Groups: []viewindex.FilterGroup{{Conds: []viewindex.Filter{{Column: "symbol", Op: pb.FilterOp_FILTER_OP_LIKE, Values: []*pb.TypedValue{{Value: &pb.TypedValue_StringValue{StringValue: "bt"}}}}}}},
		Limit:  10, TotalMode: pb.TotalMode_FORCE_EXACT,
	})
	if err != nil || len(rows) != 1 {
		t.Fatalf("like substring query: rows=%v err=%v", rows, err)
	}
}

func TestDuckDBDuplicateRowKeyUsesLastFieldValue(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := duckRowKey("s", "prices", "BTC", "1m", "2026-07-20T00:00:00Z", "venue:binance")
	value := func(v float64) *pb.FieldValue {
		return &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: v}}}
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{
			{Key: viewindex.RowKey{Key: key}, Fields: []*pb.FieldValue{value(10)}},
			{Key: viewindex.RowKey{Key: key}, Fields: []*pb.FieldValue{value(20)}},
		},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{key}, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("query rows=%v err=%v", rows, err)
	}
	if got := rows[0].GetFields()[0].GetValue().GetDoubleValue(); got != 20 {
		t.Fatalf("close = %v, want last value 20", got)
	}
}

func TestDuckDBDuplicatePhysicalRowKeyCollapsesMetadataDifferences(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	value := func(v float64) *pb.FieldValue {
		return &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: v}}}
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{
		RowWrites: []viewindex.RowWrite{
			{Key: viewindex.RowKey{Key: duckRowKey("space-a", "prices", "BTC", "1m", "2026-07-20T00:00:00Z", "venue:binance")}, Fields: []*pb.FieldValue{value(10)}},
			{Key: viewindex.RowKey{Key: duckRowKey("space-b", "other-dataset", "BTC", "1m", "2026-07-20T00:00:00Z", "venue:binance")}, Fields: []*pb.FieldValue{value(20)}},
		},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	key := duckRowKey("s", "prices", "BTC", "1m", "2026-07-20T00:00:00Z", "venue:binance")
	rows, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{key}, Limit: 1})
	if err != nil || len(rows) != 1 {
		t.Fatalf("query rows=%v err=%v", rows, err)
	}
	if got := rows[0].GetFields()[0].GetValue().GetDoubleValue(); got != 20 {
		t.Fatalf("close = %v, want last value 20", got)
	}
}

func TestPhysicalRowKeyIDAvoidsDelimiterCollisions(t *testing.T) {
	first := &pb.RowKey{Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "a", Freq: "b\x00c", DataTime: "2026-07-20T00:00:00Z", SeriesTag: "tag",
	}}}
	second := &pb.RowKey{Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "a\x00b", Freq: "c", DataTime: "2026-07-20T00:00:00Z", SeriesTag: "tag",
	}}}
	if got, want := physicalRowKeyID(first), physicalRowKeyID(second); got == want {
		t.Fatalf("physical row key IDs collide: %q", got)
	}
}

func TestPhysicalRowKeyIDCanonicalizesEquivalentTimes(t *testing.T) {
	first := &pb.RowKey{Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC", Freq: "1m", DataTime: "2026-01-01T00:00:00Z", SeriesTag: "venue:binance",
	}}}
	second := &pb.RowKey{Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
		SubjectId: "BTC", Freq: "1m", DataTime: "2026-01-01T08:00:00+08:00", SeriesTag: "venue:binance",
	}}}
	if got, want := physicalRowKeyID(first), physicalRowKeyID(second); got != want {
		t.Fatalf("equivalent RFC3339 times have different IDs: %q != %q", got, want)
	}
}

func TestDuckDBEquivalentPhysicalTimesCollapseInOneWrite(t *testing.T) {
	manager, err := OpenIndexManager(IndexManagerOptions{Root: filepath.Join(t.TempDir(), "duckdb")})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", PrimaryDatasetID: "prices", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash", Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	value := func(v float64) *pb.FieldValue {
		return &pb.FieldValue{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: v}}}
	}
	rows := []viewindex.RowWrite{
		{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", "2026-01-01T00:00:00Z", "venue:binance")}, Fields: []*pb.FieldValue{value(10)}},
		{Key: viewindex.RowKey{Key: duckRowKey("s", "prices", "BTC", "1m", "2026-01-01T08:00:00+08:00", "venue:binance")}, Fields: []*pb.FieldValue{value(20)}},
	}
	if err := manager.Write(context.Background(), "idx", viewindex.ViewIndexWriteBatch{RowWrites: rows, ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite}); err != nil {
		t.Fatal(err)
	}
	key := duckRowKey("s", "prices", "BTC", "1m", "2026-01-01T00:00:00Z", "venue:binance")
	got, _, err := manager.Query(context.Background(), "idx", viewindex.QuerySpec{Keys: []*pb.RowKey{key}, Limit: 1})
	if err != nil || len(got) != 1 {
		t.Fatalf("query rows=%v err=%v", got, err)
	}
	if value := got[0].GetFields()[0].GetValue().GetDoubleValue(); value != 20 {
		t.Fatalf("close = %v, want last value 20", value)
	}
}

func TestNormalizeColumnValueDropsMalformedJSONOnlyDuringBackfill(t *testing.T) {
	value := "map[CalleeContainerName:storage CalleeMethod:GetDataNode]"
	got, err := normalizeColumnValue(value, pb.FieldValueType_FIELD_VALUE_TYPE_JSON, viewindex.Backfill)
	if err != nil || got != nil {
		t.Fatalf("backfill malformed JSON = (%v, %v), want (nil, nil)", got, err)
	}
	if _, err := normalizeColumnValue(value, pb.FieldValueType_FIELD_VALUE_TYPE_JSON, viewindex.LiveWrite); err == nil {
		t.Fatal("live malformed JSON should fail")
	}
}
