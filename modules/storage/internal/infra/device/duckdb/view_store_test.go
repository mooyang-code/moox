//go:build cgo

package duckdb

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestOpenConstrainsDuckDBToSingleConnection(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
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
