package schema

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestValidateRecordMutationAllowsPartialUpdate(t *testing.T) {
	validator := NewValidator(recordVersionMetadata{})
	mutation := &pb.RecordMutation{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"}}
	if err := validator.ValidateRecordMutation(context.Background(), mutation, true); err != nil {
		t.Fatalf("partial update: %v", err)
	}
}

type recordVersionMetadata struct{}

func (recordVersionMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: "news", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, nil
}

func (recordVersionMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
