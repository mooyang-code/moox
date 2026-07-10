package access

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestRecordViewDefaultsToCurrent(t *testing.T) {
	service := &Service{metadataReader: recordViewMetadata{}}
	view := &pb.View{SpaceId: "crypto", PrimaryDatasetId: "news", DatasetIds: []string{"news"}}
	if err := service.normalizeAndValidateViewDatasets(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	if view.GetRecordViewMode() != pb.RecordViewMode_RECORD_VIEW_MODE_CURRENT || view.GetEngine() != "bleve" || len(view.GetGrainKeys()) != 1 {
		t.Fatalf("view = %+v", view)
	}
}

func TestHistoryRecordViewRequiresSingleDatasetAndRetention(t *testing.T) {
	service := &Service{metadataReader: recordViewMetadata{}}
	view := &pb.View{SpaceId: "crypto", PrimaryDatasetId: "news", DatasetIds: []string{"news"}, RecordViewMode: pb.RecordViewMode_RECORD_VIEW_MODE_HISTORY, RetentionWindow: "30d"}
	if err := service.normalizeAndValidateViewDatasets(context.Background(), view); err != nil {
		t.Fatal(err)
	}
	if len(view.GetGrainKeys()) != 2 || view.GetGrainKeys()[1] != "revision" {
		t.Fatalf("grain keys = %v", view.GetGrainKeys())
	}
	view.DatasetIds = []string{"news", "profiles"}
	if err := service.normalizeAndValidateViewDatasets(context.Background(), view); err == nil {
		t.Fatal("expected multiple dataset history rejection")
	}
}

func TestRecordViewRejectsFixedFilter(t *testing.T) {
	service := &Service{metadataReader: recordViewMetadata{}}
	view := &pb.View{SpaceId: "crypto", PrimaryDatasetId: "news", DatasetIds: []string{"news"}, FilterJson: `{"status":"active"}`}
	if err := service.normalizeAndValidateViewDatasets(context.Background(), view); err == nil {
		t.Fatal("expected Record filter rejection")
	}
}

type recordViewMetadata struct{ metadata.Reader }

func (recordViewMetadata) GetDataset(_ context.Context, _ string, datasetID string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, nil
}
func (recordViewMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
