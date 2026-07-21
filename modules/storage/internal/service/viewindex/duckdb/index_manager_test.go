//go:build cgo

package duckdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestDuckDBCreatesValidDatabaseAndPersistsRows(t *testing.T) {
	root := filepath.Join(t.TempDir(), "duckdb")
	manager, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", ViewVersion: 1, Engine: "duckdb", SchemaHash: "hash"}
	if err := manager.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "prices", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-20T00:00:00Z"}}}
	if err := manager.Apply(context.Background(), "idx", viewindex.ViewIndexApplyBatch{
		RowWrites: []viewindex.RowWrite{{
			Key:    viewindex.RowKey{Key: key},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}}}},
		}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", filepath.Join(root, "idx.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM view_rows`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if count != 1 {
		t.Fatalf("count=%d", count)
	}
	restarted, err := OpenIndexManager(IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := restarted.Query(context.Background(), "idx", []*pb.RowKey{key}, []string{"close"})
	if err != nil || len(rows) != 1 || rows[0].GetFields()[0].GetValue().GetDoubleValue() != 100 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
