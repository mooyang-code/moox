package bleve

import (
	"context"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestIndexRowsMergesPartialRecordPatches(t *testing.T) {
	ctx := context.Background()
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "records.bleve")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer index.Close()

	closePatch := bleveTestRecordRow("crypto", "kline", "BTC-USDT", "v1", bleveTestValue("close", 12))
	if err := index.IndexRows(ctx, []*pb.RecordRow{closePatch}, map[string]bool{"close": true, "volume": true}); err != nil {
		t.Fatalf("IndexRows close: %v", err)
	}
	volumePatch := bleveTestRecordRow("crypto", "kline", "BTC-USDT", "v1", bleveTestValue("volume", 99))
	if err := index.IndexRows(ctx, []*pb.RecordRow{volumePatch}, map[string]bool{"close": true, "volume": true}); err != nil {
		t.Fatalf("IndexRows volume: %v", err)
	}

	rows, _, err := index.SearchRecordRows(ctx, SearchRequest{
		SpaceID:   "crypto",
		DatasetID: "kline",
		RecordIDs: []string{"BTC-USDT"},
	})
	if err != nil {
		t.Fatalf("SearchRecordRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := bleveColumnDouble(rows[0], "close"); got != 12 {
		t.Fatalf("close = %v, want 12", got)
	}
	if got := bleveColumnDouble(rows[0], "volume"); got != 99 {
		t.Fatalf("volume = %v, want 99", got)
	}
}

func bleveTestRecordRow(spaceID, datasetID, recordID, version string, columns ...*pb.ColumnValue) *pb.RecordRow {
	return &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			RecordId:  recordID,
			Version:   version,
		},
		Columns: columns,
	}
}

func bleveTestValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func bleveColumnDouble(row *pb.RecordRow, name string) float64 {
	for _, column := range row.GetColumns() {
		if column.GetColumnName() == name {
			return column.GetValue().GetDoubleValue()
		}
	}
	return 0
}
