//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestOpenAllowsSpareConnectionsForConcurrentReads(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if got := store.db.Stats().MaxOpenConnections; got != defaultMaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, defaultMaxOpenConns)
	}
}

func TestDropResultTableWaitsForResultTableLock(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
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
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
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

func TestInsertRowsAllowsHalfRowAndPreservesMissingPatchColumns(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	columns := []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	if err := store.CreateResultTable(ctx, "test_view_half_row", columns); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}

	patchClose := duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("close", 2))
	if err := store.InsertRows(ctx, "test_view_half_row", []*pb.TimeSeriesRow{patchClose}); err != nil {
		t.Fatalf("InsertRows close patch: %v", err)
	}
	assertDuckDBValues(t, store, "test_view_half_row", 2, nil)

	patchVolume := duckDBTestRow("BTC-USDT", "2026-07-07T04:49:00Z", duckDBTestValue("volume", 11))
	if err := store.InsertRows(ctx, "test_view_half_row", []*pb.TimeSeriesRow{patchVolume}); err != nil {
		t.Fatalf("InsertRows volume patch: %v", err)
	}
	assertDuckDBValues(t, store, "test_view_half_row", 2, floatPtr(11))

	if err := store.InsertRows(ctx, "test_view_half_row", []*pb.TimeSeriesRow{patchClose}); err != nil {
		t.Fatalf("InsertRows close patch again: %v", err)
	}
	assertDuckDBValues(t, store, "test_view_half_row", 2, floatPtr(11))
}

func assertDuckDBValues(t *testing.T, store *ViewStore, tableName string, wantClose float64, wantVolume *float64) {
	t.Helper()
	var count int
	var closeValue float64
	var volumeValue sql.NullFloat64
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*), MAX(close), MAX(volume)
		FROM `+tableName+`
		WHERE subject_id = 'BTC-USDT' AND data_time = '2026-07-07T04:49:00.000000000Z'
	`).Scan(&count, &closeValue, &volumeValue); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if count != 1 || closeValue != wantClose {
		t.Fatalf("row = count:%d close:%v, want count:1 close:%v", count, closeValue, wantClose)
	}
	if wantVolume == nil {
		if volumeValue.Valid {
			t.Fatalf("volume = %v, want NULL", volumeValue.Float64)
		}
		return
	}
	if !volumeValue.Valid || volumeValue.Float64 != *wantVolume {
		t.Fatalf("volume = %+v, want %v", volumeValue, *wantVolume)
	}
}

func floatPtr(value float64) *float64 {
	return &value
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

func duckDBTestValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}
