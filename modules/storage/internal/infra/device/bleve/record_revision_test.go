package bleve

import (
	"context"
	"path/filepath"
	"testing"

	coreviewindex "github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestBleveCurrentUsesOneDocumentPerRecordAndRejectsOlderOrder(t *testing.T) {
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "index")})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.SetSchema(context.Background(), coreviewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "view", ViewVersion: 1, Engine: "bleve", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT, LayoutRevision: 2, SchemaHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	mutations := []*pb.RecordIndexMutation{{Row: &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "r"}, Revision: 2, CommitSeq: 2}, OrderCommitSeq: 2, SourceId: "source"}}
	if err := index.ApplyRecordMutations(context.Background(), mutations, nil, pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT); err != nil {
		t.Fatal(err)
	}
	if err := index.ApplyRecordMutations(context.Background(), []*pb.RecordIndexMutation{{Row: &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "r"}, Revision: 1, CommitSeq: 1}, OrderCommitSeq: 1, SourceId: "source"}}, nil, pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT); err != nil {
		t.Fatal(err)
	}
	rows, _, err := index.SearchRecordRows(context.Background(), SearchRequest{SpaceID: "crypto", DatasetID: "news", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT})
	if err != nil || len(rows) != 1 || rows[0].GetRevision() != 2 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestBleveHistoryUsesOneDocumentPerRevisionAndNumericRange(t *testing.T) {
	index, err := Open(Options{Path: filepath.Join(t.TempDir(), "index")})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	if err := index.SetSchema(context.Background(), coreviewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "view", ViewVersion: 1, Engine: "bleve", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY, LayoutRevision: 2, SchemaHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []uint64{2, 10} {
		if err := index.ApplyRecordMutations(context.Background(), []*pb.RecordIndexMutation{{Row: &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "r"}, Revision: revision, CommitSeq: revision}, OrderCommitSeq: revision, SourceId: "source"}}, nil, pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := index.SearchRecordRows(context.Background(), SearchRequest{SpaceID: "crypto", DatasetID: "news", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY, RevisionRange: &pb.RevisionRange{StartRevision: 10, EndRevision: 10}})
	if err != nil || len(rows) != 1 || rows[0].GetRevision() != 10 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
