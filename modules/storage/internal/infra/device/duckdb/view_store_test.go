//go:build cgo

package duckdb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

func TestOpenUsesSingleConnectionToAvoidDuckDBFileLockContention(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if got := store.db.Stats().MaxOpenConnections; got != defaultMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, defaultMaxOpenConns)
	}
}

func TestDuckDBDSNAddsResourceLimits(t *testing.T) {
	t.Setenv(duckDBMemoryLimitEnv, "256MB")
	t.Setenv(duckDBThreadsEnv, "1")
	t.Setenv(duckDBMaxTempDirectorySizeEnv, "1GB")

	dsn := duckDBDSN("index.duckdb?access_mode=read_only")
	for _, want := range []string{
		"access_mode=read_only",
		"memory_limit=256MB",
		"threads=1",
		"max_temp_directory_size=1GB",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("duckDBDSN() = %q, want %q", dsn, want)
		}
	}
}

func TestDuckDBDSNDoesNotOverrideExplicitResourceLimits(t *testing.T) {
	t.Setenv(duckDBMemoryLimitEnv, "256MB")

	dsn := duckDBDSN("index.duckdb?memory_limit=128MB")
	if strings.Contains(dsn, "memory_limit=256MB") {
		t.Fatalf("duckDBDSN() = %q, should keep explicit memory_limit", dsn)
	}
	if !strings.Contains(dsn, "memory_limit=128MB") {
		t.Fatalf("duckDBDSN() = %q, want explicit memory_limit", dsn)
	}
}

func TestDropResultTableWaitsForResultTableLock(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const tableName = "test_view_drop_lock"
	if err := store.CreateResultTable(ctx, tableName, []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}

	unlock := store.lockResultTable(tableName)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- store.DropResultTable(ctx, tableName)
	}()
	<-started

	select {
	case err := <-done:
		unlock()
		if err != nil {
			t.Fatalf("DropResultTable returned error while lock held: %v", err)
		}
		t.Fatalf("DropResultTable returned while table lock was held")
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("DropResultTable after unlock: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("DropResultTable did not finish after table lock was released")
	}
}

func TestInsertRowsMergesExistingAndDuplicateBatchRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	columns := []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	if err := store.CreateResultTable(ctx, "test_view", columns); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	base := duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z",
		duckDBTestValue("close", 1),
		duckDBTestValue("volume", 10),
	)
	if err := store.InsertRows(ctx, "test_view", []*pb.TimeSeriesRow{base}); err != nil {
		t.Fatalf("InsertRows base: %v", err)
	}

	patchClose := duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 2))
	patchVolume := duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("volume", 11))
	if err := store.InsertRows(ctx, "test_view", []*pb.TimeSeriesRow{patchClose, patchVolume}); err != nil {
		t.Fatalf("InsertRows patch: %v", err)
	}

	var count int
	var closeValue, volumeValue float64
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(close), MAX(volume)
		FROM test_view
		WHERE subject_id = 'BTC-USDT' AND data_time = '2026-07-07T04:49:00.000000000Z'
	`).Scan(&count, &closeValue, &volumeValue); err != nil {
		t.Fatalf("query merged row: %v", err)
	}
	if count != 1 || closeValue != 2 || volumeValue != 11 {
		t.Fatalf("merged row = count:%d close:%v volume:%v, want count:1 close:2 volume:11", count, closeValue, volumeValue)
	}
}

func TestDuckDBViewIndexPrepareWriteStatRemove(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if got := store.Engine(); got != "duckdb" {
		t.Fatalf("Engine() = %q, want duckdb", got)
	}
	indexID := "view_crypto_spot_kline_1m_view_a"
	schema := viewindex.ViewIndexSchema{
		SpaceID: "crypto",
		ViewID:  "spot_kline_1m_view",
		Engine:  "duckdb",
		Columns: []*pb.ViewColumn{
			{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
			{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		},
	}
	if err := store.CreateResultTable(ctx, indexID, schema.Columns[:1]); err != nil {
		t.Fatalf("CreateResultTable stale: %v", err)
	}
	if err := store.InsertRows(ctx, indexID, []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
	}); err != nil {
		t.Fatalf("InsertRows stale: %v", err)
	}

	if err := store.Prepare(ctx, indexID, schema); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	stats, err := store.Stat(ctx, indexID)
	if err != nil {
		t.Fatalf("Stat after Prepare: %v", err)
	}
	if !stats.Exists || stats.EntryCount != 0 || stats.MinVersion != "" || stats.MaxVersion != "" {
		t.Fatalf("stats after Prepare = %+v, want empty existing table", stats)
	}

	err = store.Write(ctx, indexID, viewindex.ViewIndexBatch{RecordRows: []*pb.RecordRow{{}}})
	if err == nil || !strings.Contains(err.Error(), "rejects record rows") {
		t.Fatalf("Write record rows error = %v, want rejection", err)
	}
	if err := store.Write(ctx, indexID, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 2), duckDBTestValue("volume", 20)),
		duckDBTestRow("ETH-USDT", "2026-07-07T04:51:00Z", duckDBTestValue("close", 3), duckDBTestValue("volume", 30)),
	}}); err != nil {
		t.Fatalf("Write time series rows: %v", err)
	}
	stats, err = store.Stat(ctx, indexID)
	if err != nil {
		t.Fatalf("Stat after Write: %v", err)
	}
	if !stats.Exists || stats.EntryCount != 2 {
		t.Fatalf("stats after Write = %+v, want 2 rows", stats)
	}
	if stats.MinVersion != "2026-07-07T04:50:00.000000000Z" || stats.MaxVersion != "2026-07-07T04:51:00.000000000Z" {
		t.Fatalf("version range = %q/%q, want normalized min/max", stats.MinVersion, stats.MaxVersion)
	}

	if err := store.Remove(ctx, indexID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	stats, err = store.Stat(ctx, indexID)
	if err != nil {
		t.Fatalf("Stat after Remove: %v", err)
	}
	if stats.Exists {
		t.Fatalf("stats after Remove = %+v, want missing table", stats)
	}
	var metadataRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM moox_view_columns WHERE table_name = ?`, indexID).Scan(&metadataRows); err != nil {
		t.Fatalf("query column metadata: %v", err)
	}
	if metadataRows != 0 {
		t.Fatalf("column metadata rows = %d, want 0", metadataRows)
	}
}

func TestDuckDBViewIndexReplayDoesNotIncreaseEntryCount(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const indexID = "view_crypto_replay_a"
	schema := viewindex.ViewIndexSchema{
		SpaceID: "crypto", ViewID: "replay", Engine: "duckdb",
		Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	if err := store.Prepare(ctx, indexID, schema); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	batch := viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 2)),
	}}
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.Write(ctx, indexID, batch); err != nil {
			t.Fatalf("Write attempt %d: %v", attempt+1, err)
		}
	}
	stats, err := store.Stat(ctx, indexID)
	if err != nil || stats.EntryCount != 1 {
		t.Fatalf("stats=%+v err=%v, want one replay-safe row", stats, err)
	}
}

func TestDuckDBApplyIsAtomicAndPersistsCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const indexID = "view_crypto_apply_a"
	schema := viewindex.ViewIndexSchema{
		SpaceID: "crypto", ViewID: "apply", Engine: "duckdb",
		Columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	if err := store.Prepare(ctx, indexID, schema); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	base := duckDBTestRow("BTC-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 1))
	if err := store.Apply(ctx, indexID, viewindex.ViewIndexApplyBatch{RowWrites: []viewindex.RowWrite{{
		Operation: viewindex.RowWriteOperationReplace,
		Key:       viewindex.RowKey{TimeSeriesKey: base.GetKey()},
		Columns:   base.GetColumns(),
	}}}); err != nil {
		t.Fatalf("initial Apply: %v", err)
	}
	missing := duckDBTestRow("ETH-USDT", "2026-07-07T04:51:00Z", duckDBTestValue("close", 2))
	err = store.Apply(ctx, indexID, viewindex.ViewIndexApplyBatch{
		RowWrites: []viewindex.RowWrite{
			{Operation: viewindex.RowWriteOperationMerge, Key: viewindex.RowKey{TimeSeriesKey: base.GetKey()}, Columns: []*pb.ColumnValue{{ColumnName: "close", Value: duckDBTestValue("close", 3).GetValue()}}},
			{Operation: viewindex.RowWriteOperationMerge, Key: viewindex.RowKey{TimeSeriesKey: missing.GetKey()}, Columns: missing.GetColumns()},
		},
		CheckpointUpdates: []viewindex.ShardCheckpointUpdate{{ShardID: "shard-1", ExpectedLastAppliedSequence: 0, LastAppliedSequence: 1}},
	})
	var missingErr *viewindex.MissingRowsError
	if !errors.As(err, &missingErr) || len(missingErr.TimeSeriesKeys) != 1 {
		t.Fatalf("Apply missing error = %v, want one missing key", err)
	}
	stats, err := store.Stat(ctx, indexID)
	if err != nil {
		t.Fatalf("Stat after failed Apply: %v", err)
	}
	if stats.EntryCount != 1 || len(stats.ShardCheckpoints) != 0 {
		t.Fatalf("failed Apply changed state: %+v", stats)
	}

	if err := store.Apply(ctx, indexID, viewindex.ViewIndexApplyBatch{
		RowWrites:         []viewindex.RowWrite{{Operation: viewindex.RowWriteOperationReplace, Key: viewindex.RowKey{TimeSeriesKey: missing.GetKey()}, Columns: missing.GetColumns()}},
		CheckpointUpdates: []viewindex.ShardCheckpointUpdate{{ShardID: "shard-1", ExpectedLastAppliedSequence: 0, LastAppliedSequence: 1}},
	}); err != nil {
		t.Fatalf("recovery Apply: %v", err)
	}
	stats, err = store.Stat(ctx, indexID)
	if err != nil || stats.EntryCount != 2 || stats.ShardCheckpoints["shard-1"] != 1 {
		t.Fatalf("recovered stats=%+v err=%v", stats, err)
	}
}

func TestListResultTablesIncludesViewPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	const tableName = "view_crypto_spot_kline_1m_view_a"
	if err := store.CreateResultTable(ctx, tableName, []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, tableName, []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	if _, _, _, err := store.QueryTimeSeriesRows(ctx, tableName, previewRequest(1)); err != nil {
		t.Fatalf("QueryTimeSeriesRows latest: %v", err)
	}

	tables, err := store.ListResultTables(ctx)
	if err != nil {
		t.Fatalf("ListResultTables: %v", err)
	}
	if !hasString(tables, tableName) {
		t.Fatalf("ListResultTables = %v, want %s", tables, tableName)
	}
}

func TestInsertRowsWaitsForDatabaseWriteLock(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view_write_lock", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}

	store.writeMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- store.InsertRows(ctx, "test_view_write_lock", []*pb.TimeSeriesRow{
			duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
		})
	}()
	<-started

	select {
	case err := <-done:
		store.writeMu.Unlock()
		if err != nil {
			t.Fatalf("InsertRows returned error while write lock held: %v", err)
		}
		t.Fatal("InsertRows returned while database write lock was held")
	case <-time.After(100 * time.Millisecond):
	}

	store.writeMu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InsertRows after unlock: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("InsertRows did not finish after write lock was released")
	}
}

func TestBuildTimeSeriesQueryPreviewLimitSkipsCount(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Limit:     1000,
		TotalMode: pb.TotalMode_AUTO,
		Sorts: []*pb.SortSpec{{
			FieldName: "data_time",
			Desc:      true,
		}},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL != "" {
		t.Fatalf("countSQL = %q, want empty", plan.countSQL)
	}
	if !strings.Contains(plan.keySQL, "LIMIT 1001 OFFSET 0") {
		t.Fatalf("keySQL = %q, want limit probe", plan.keySQL)
	}
	if plan.pageNo != 1 || plan.size != 1000 || !plan.preview {
		t.Fatalf("plan page=%d size=%d preview=%v, want 1/1000/true", plan.pageNo, plan.size, plan.preview)
	}
	if plan.totalState != pb.TotalState_SKIPPED {
		t.Fatalf("totalState = %v, want skipped", plan.totalState)
	}
}

func TestBuildTimeSeriesQueryDefaultsPageSizeTo25(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.pageNo != 1 || plan.size != 25 {
		t.Fatalf("page=%d size=%d, want 1/25", plan.pageNo, plan.size)
	}
	if !strings.Contains(plan.sqlText, "LIMIT 26 OFFSET 0") {
		t.Fatalf("sqlText = %q, want size+1 probe", plan.sqlText)
	}
}

func TestBuildTimeSeriesQueryDoesNotSortWhenSortsAreEmpty(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{Limit: 1000})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if strings.Contains(plan.sqlText, "ORDER BY") {
		t.Fatalf("sqlText = %q, want no ORDER BY without explicit sorts", plan.sqlText)
	}
}

func TestBuildTimeSeriesQuerySortsPageKeysBeforeFetchingRows(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Limit:     1000,
		Page:      &pb.Page{Page: 1, Size: 5},
		TotalMode: pb.TotalMode_NONE,
		Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.keySQL == "" {
		t.Fatalf("keySQL is empty, want ordered query to page row keys first")
	}
	for _, want := range []string{
		"SELECT \"row_key\",\"data_time\" FROM view_test",
		"ORDER BY \"data_time\" DESC",
		"LIMIT 6) AS moox_limited_keys",
		"LIMIT 6 OFFSET 0",
	} {
		if !strings.Contains(plan.keySQL, want) {
			t.Fatalf("keySQL = %q, want %q", plan.keySQL, want)
		}
	}
	if strings.Contains(plan.keySQL, "row_json") || strings.Contains(plan.keySQL, "attributes_json") {
		t.Fatalf("keySQL = %q, should not carry wide row payload through ORDER BY", plan.keySQL)
	}
}

func TestBuildTimeSeriesQueryCapsSkippedCountInnerLimitToCurrentPageWindow(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Limit:     1000,
		Page:      &pb.Page{Page: 2, Size: 25},
		TotalMode: pb.TotalMode_NONE,
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.pageNo != 2 || plan.size != 25 || plan.preview {
		t.Fatalf("plan page=%d size=%d preview=%v, want 2/25/false", plan.pageNo, plan.size, plan.preview)
	}
	for _, want := range []string{
		"FROM (SELECT",
		"LIMIT 51) AS moox_limited",
		"LIMIT 26 OFFSET 25",
	} {
		if !strings.Contains(plan.sqlText, want) {
			t.Fatalf("sqlText = %q, want %q", plan.sqlText, want)
		}
	}
}

func TestBuildTimeSeriesQueryAutoCountsBoundedQuery(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT", Freq: "1m"}},
		Page: &pb.Page{Page: 1, Size: 25},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL == "" {
		t.Fatalf("countSQL is empty, want exact count for bounded AUTO query")
	}
	if plan.totalState != pb.TotalState_EXACT {
		t.Fatalf("totalState = %v, want exact", plan.totalState)
	}
}

func TestBuildTimeSeriesQueryLimitsExactCountForSortedPagedQuery(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Limit:     1000,
		Page:      &pb.Page{Page: 1, Size: 25},
		TotalMode: pb.TotalMode_FORCE_EXACT,
		Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if !strings.Contains(plan.countSQL, "LIMIT 1000") {
		t.Fatalf("countSQL = %q, want count bounded by request limit", plan.countSQL)
	}
}

func TestBuildKeyPredicatesUsesRowKeyPrefixForCompleteKey(t *testing.T) {
	where, args, err := buildKeyPredicates([]*pb.TimeSeriesKey{{
		SpaceId:   "crypto",
		DatasetId: "binance_spot_kline",
		SubjectId: "BTC-USDT",
		Freq:      "1m",
	}})
	if err != nil {
		t.Fatalf("buildKeyPredicates: %v", err)
	}
	if !strings.Contains(where, "row_key LIKE ? ESCAPE '\\'") {
		t.Fatalf("where = %q, want row_key prefix predicate", where)
	}
	for _, notWant := range []string{"dataset_id = ?", "subject_id = ?", "freq = ?"} {
		if strings.Contains(where, notWant) {
			t.Fatalf("where = %q, should avoid redundant %q predicates when row_key prefix is available", where, notWant)
		}
	}
	if len(args) != 2 || args[0] != "crypto" || args[1] != `binance\_spot\_kline|BTC-USDT|1m|%` {
		t.Fatalf("args = %#v, want space and row_key prefix", args)
	}
}

func TestBuildTimeSeriesQueryAutoSkipsCountWhenRequestHasNoEffectivePredicate(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Keys:      []*pb.TimeSeriesKey{{}},
		TimeRange: &pb.TimeRange{},
		Page:      &pb.Page{Page: 1, Size: 25},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL != "" {
		t.Fatalf("countSQL = %q, want skipped count when predicates are empty", plan.countSQL)
	}
	if plan.totalState != pb.TotalState_SKIPPED {
		t.Fatalf("totalState = %v, want skipped", plan.totalState)
	}
}

func TestBuildTimeSeriesQueryExactCountsUnboundedQuery(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		TotalMode: pb.TotalMode_FORCE_EXACT,
		Page:      &pb.Page{Page: 1, Size: 25},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL == "" {
		t.Fatalf("countSQL is empty, want exact count for FORCE_EXACT")
	}
	if !strings.Contains(plan.sqlText, "LIMIT 25 OFFSET 0") {
		t.Fatalf("sqlText = %q, want normal page limit without +1 probe", plan.sqlText)
	}
}

func TestBuildTimeSeriesQueryProjectsRequestedColumns(t *testing.T) {
	columns := []*pb.ResultColumn{
		{ColumnName: "open", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "high", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "low", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	plan, err := buildTimeSeriesQuery("view_test", columns, &pb.QueryTimeSeriesRowsReq{
		ColumnNames: []string{"close"},
		Limit:       1000,
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}

	if !strings.Contains(plan.sqlText, quoteIdentForTest("close")) {
		t.Fatalf("sqlText = %q, want requested close column", plan.sqlText)
	}
	for _, name := range []string{"open", "high", "low", "volume"} {
		if strings.Contains(plan.sqlText, quoteIdentForTest(name)) {
			t.Fatalf("sqlText = %q, should not select unrequested column %s", plan.sqlText, name)
		}
	}
	for _, name := range []string{"space_id", "dataset_id", "subject_id", "freq", "data_time"} {
		if !strings.Contains(plan.sqlText, quoteIdentForTest(name)) {
			t.Fatalf("sqlText = %q, want system column %s", plan.sqlText, name)
		}
	}
}

func TestCreateResultIndexStatementsIncludesDataTimeIndex(t *testing.T) {
	statements, err := createResultIndexStatements("view_test", nil)
	if err != nil {
		t.Fatalf("createResultIndexStatements: %v", err)
	}
	joined := strings.Join(statements, "\n")
	if !strings.Contains(joined, quoteIdentForTest("idx_view_test_data_time")) ||
		!strings.Contains(joined, "ON \"view_test\" (data_time)") {
		t.Fatalf("index statements = %q, want standalone data_time index", joined)
	}
}

func TestQueryTimeSeriesRowsDoesNotCreateIndexesOnRead(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	tableName := "test_view_no_read_index"
	if err := store.CreateResultTable(ctx, tableName, []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, tableName, []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	statements, err := dropResultIndexStatements(tableName, []resultColumnDef{{name: "close", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}})
	if err != nil {
		t.Fatalf("dropResultIndexStatements: %v", err)
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("drop index %q: %v", statement, err)
		}
	}
	store.indexedTables.Delete(tableName)

	if _, _, _, err := store.QueryTimeSeriesRows(ctx, tableName, &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT", Freq: "1m"}},
		Page: &pb.Page{Page: 1, Size: 1},
	}); err != nil {
		t.Fatalf("QueryTimeSeriesRows: %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM duckdb_indexes() WHERE table_name = ?`, tableName).Scan(&count); err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if count != 0 {
		t.Fatalf("read query recreated %d DuckDB index(es), want 0", count)
	}
}

func TestQueryTimeSeriesRowsMatchesCompleteKeyWithEscapedRowKeyPrefix(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view_row_key_prefix", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_row_key_prefix", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
		duckDBTestRow("BTC-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 2)),
		duckDBTestRow("ETH-USDT", "2026-07-07T04:51:00Z", duckDBTestValue("close", 3)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}

	_, rows, _, err := store.QueryTimeSeriesRows(ctx, "test_view_row_key_prefix", &pb.QueryTimeSeriesRowsReq{
		ColumnNames: []string{"close"},
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:   "crypto",
			DatasetId: "binance_spot_kline",
			SubjectId: "BTC-USDT",
			Freq:      "1m",
		}},
		Page:      &pb.Page{Page: 1, Size: 1},
		Limit:     1000,
		TotalMode: pb.TotalMode_NONE,
		Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
	})
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows: %v", err)
	}
	if len(rows) != 1 || rows[0].GetKey().GetSubjectId() != "BTC-USDT" ||
		rows[0].GetKey().GetDataTime() != "2026-07-07T04:50:00.000000000Z" {
		t.Fatalf("rows = %+v, want latest BTC row", rows)
	}
}

func TestQueryTimeSeriesRowsGlobalDataTimeDescPreview(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view_preview", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_preview", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
		duckDBTestRow("ETH-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 2)),
		duckDBTestRow("SOL-USDT", "2026-07-07T04:51:00Z", duckDBTestValue("close", 3)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}

	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "test_view_preview", &pb.QueryTimeSeriesRowsReq{
		ColumnNames: []string{"close"},
		Limit:       2,
		TotalMode:   pb.TotalMode_NONE,
		Sorts:       []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
	})
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows: %v", err)
	}
	if len(rows) != 2 || rows[0].GetKey().GetSubjectId() != "SOL-USDT" || rows[1].GetKey().GetSubjectId() != "ETH-USDT" {
		t.Fatalf("rows = %+v, want latest SOL then ETH", rows)
	}
	if page == nil || !page.GetHasMore() || page.GetTotalState() != pb.TotalState_SKIPPED {
		t.Fatalf("page = %+v, want skipped preview with has_more", page)
	}
}

func TestQueryTimeSeriesRowsReturnsLimitSizedPage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view_page", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_page", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
		duckDBTestRow("ETH-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 2)),
		duckDBTestRow("SOL-USDT", "2026-07-07T04:51:00Z", duckDBTestValue("close", 3)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	_, rows, page, err := store.QueryTimeSeriesRows(ctx, "test_view_page", &pb.QueryTimeSeriesRowsReq{
		ColumnNames: []string{"close"},
		Limit:       2,
		Page:        &pb.Page{Page: 1, Size: 2},
		TotalMode:   pb.TotalMode_NONE,
		Sorts:       []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
	})
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows: %v", err)
	}
	if len(rows) != 2 || rows[0].GetKey().GetSubjectId() != "SOL-USDT" || rows[1].GetKey().GetSubjectId() != "ETH-USDT" {
		t.Fatalf("rows = %+v, want latest SOL then ETH", rows)
	}
	if page == nil || page.GetHasMore() {
		t.Fatalf("page = %+v, want no has_more inside limited page", page)
	}
}

func TestQueryTimeSeriesRowsNoSortPreview(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view_no_sort", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_no_sort", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
		duckDBTestRow("ETH-USDT", "2026-07-07T04:50:00Z", duckDBTestValue("close", 2)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	if _, _, _, err := store.QueryTimeSeriesRows(ctx, "test_view_no_sort", &pb.QueryTimeSeriesRowsReq{
		Limit:     2,
		TotalMode: pb.TotalMode_NONE,
	}); err != nil {
		t.Fatalf("QueryTimeSeriesRows: %v", err)
	}
}

func TestQueryTimeSeriesRowsReadsNewRowsAfterRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "index.duckdb")
	store, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open initial: %v", err)
	}
	if err := store.CreateResultTable(ctx, "test_view_restart", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_restart", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1)),
	}); err != nil {
		t.Fatalf("InsertRows initial: %v", err)
	}
	if _, _, _, err := store.QueryTimeSeriesRows(ctx, "test_view_restart", previewRequest(1)); err != nil {
		t.Fatalf("QueryTimeSeriesRows initial: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close initial: %v", err)
	}

	restarted, err := Open(Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open restarted: %v", err)
	}
	defer restarted.Close()
	if err := restarted.InsertRows(ctx, "test_view_restart", []*pb.TimeSeriesRow{
		duckDBTestRow("ETH-USDT", "2026-07-07T04:55:00Z", duckDBTestValue("close", 2)),
	}); err != nil {
		t.Fatalf("InsertRows after restart: %v", err)
	}

	_, rows, _, err := restarted.QueryTimeSeriesRows(ctx, "test_view_restart", previewRequest(1))
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows after restart: %v", err)
	}
	if len(rows) != 1 || rows[0].GetKey().GetSubjectId() != "ETH-USDT" {
		t.Fatalf("rows = %+v, want ETH latest after restart", rows)
	}
}

func TestMergeRowsIntoTableReturnsMergedRows(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view_merge", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_merge", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 1), duckDBTestValue("volume", 10)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	columns, err := store.loadColumns(ctx, "test_view_merge")
	if err != nil {
		t.Fatalf("loadColumns: %v", err)
	}
	quoted, err := quoteTableName("test_view_merge")
	if err != nil {
		t.Fatalf("quoteTableName: %v", err)
	}

	merged, err := store.mergeRowsIntoTable(ctx, quoted, columns, []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 2)),
	})
	if err != nil {
		t.Fatalf("mergeRowsIntoTable: %v", err)
	}
	if len(merged) != 1 {
		t.Fatalf("merged rows = %d, want 1", len(merged))
	}
	values := map[string]float64{}
	for _, column := range merged[0].GetColumns() {
		values[column.GetColumnName()] = column.GetValue().GetDoubleValue()
	}
	if values["close"] != 2 || values["volume"] != 10 {
		t.Fatalf("merged values = %+v, want close=2 volume=10", values)
	}
}

func duckDBTestRow(subjectID string, dataTime string, columns ...*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:   "crypto",
			DatasetId: "binance_spot_kline",
			SubjectId: subjectID,
			Freq:      "1m",
			DataTime:  dataTime,
		},
		Columns: columns,
	}
}

func previewRequest(limit uint32) *pb.QueryTimeSeriesRowsReq {
	return &pb.QueryTimeSeriesRowsReq{
		ColumnNames: []string{"close"},
		Limit:       limit,
		TotalMode:   pb.TotalMode_NONE,
		Sorts:       []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
	}
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func quoteIdentForTest(name string) string {
	return `"` + name + `"`
}

func duckDBTestValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func TestQueryTimeSeriesRowsUsesExplicitSpareConnectionWhenOneConnectionIsBusy(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "index.duckdb"), MaxOpenConns: 2})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	const tableName = "view_pool_test"
	err = store.CreateResultTable(context.Background(), tableName, []*pb.ViewColumn{
		{
			ColumnName: "dataset.close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "dataset.close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		},
	})
	if err != nil {
		t.Fatalf("CreateResultTable() error = %v", err)
	}

	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer tx.Rollback()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, _, err = store.QueryTimeSeriesRows(ctx, tableName, &pb.QueryTimeSeriesRowsReq{
		Page: &commonpb.Page{Page: 1, Size: 5},
	})
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows() should not wait behind an unrelated busy connection: %v", err)
	}
}
