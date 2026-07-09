//go:build cgo

package duckdb

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
)

func TestQueryTimeSeriesRowsUsesExplicitSpareConnectionWhenOneConnectionIsBusy(t *testing.T) {
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb"), MaxOpenConns: 2})
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
