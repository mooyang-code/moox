package view

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestCurrentRecordProjectionUsesPrimaryRevisionMetadata(t *testing.T) {
	view := &pb.View{PrimaryDatasetId: "primary", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT, Columns: []*pb.ViewColumn{{ColumnName: "primary.name", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "primary.name"}}}
	mutation, err := BuildCurrentRecordMutation(view, map[string]*pb.RecordRow{"primary": {Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "primary", RecordId: "r"}, Revision: 3, CommitSeq: 9}}, "source-1", 9)
	if err != nil || mutation.GetRow().GetRevision() != 3 || mutation.GetOrderCommitSeq() != 9 || mutation.GetSourceId() != "source-1" {
		t.Fatalf("mutation=%+v err=%v", mutation, err)
	}
}

func TestCurrentRecordProjectionReturnsNoopWhenPrimaryMissing(t *testing.T) {
	view := &pb.View{PrimaryDatasetId: "primary", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT}
	mutation, err := BuildCurrentRecordMutation(view, map[string]*pb.RecordRow{}, "source-1", 1)
	if err != nil || mutation != nil {
		t.Fatalf("mutation=%+v err=%v", mutation, err)
	}
}

func TestHistoryRecordMappingPreservesEveryCommittedRevision(t *testing.T) {
	view := &pb.View{PrimaryDatasetId: "primary", RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY, DatasetIds: []string{"primary"}}
	row := &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "primary", RecordId: "r"}, Revision: 2, CommitSeq: 8}
	mutation, err := BuildHistoryRecordMutation(view, row, "source-1")
	if err != nil || mutation.GetRow().GetRevision() != 2 || mutation.GetOrderCommitSeq() != 8 {
		t.Fatalf("mutation=%+v err=%v", mutation, err)
	}
}
