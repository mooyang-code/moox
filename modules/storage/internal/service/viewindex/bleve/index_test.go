package bleve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestBlevePersistsRowsAndSupportsFullTextSearch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bleve")
	index, err := Open(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	schema := viewindex.ViewIndexSchema{SpaceID: "s", ViewID: "v", ViewVersion: 1, Engine: "bleve", SchemaHash: "hash"}
	if err := index.Prepare(context.Background(), "idx", schema); err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	if err := index.Apply(context.Background(), "idx", viewindex.ViewIndexApplyBatch{
		RowWrites: []viewindex.RowWrite{{
			Key:    viewindex.RowKey{Key: key},
			Fields: []*pb.FieldValue{{FieldId: "title", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "quant research note"}}}},
		}},
		ViewRevision: 1, ViewSchemaHash: "hash", WriteMode: viewindex.LiveWrite,
	}); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(Options{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := restarted.Search(context.Background(), "idx", "research", []string{"title"}, 10)
	if err != nil || len(rows) != 1 || rows[0].GetFields()[0].GetValue().GetStringValue() != "quant research note" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
