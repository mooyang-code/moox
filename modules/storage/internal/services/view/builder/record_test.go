package builder

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestRecordBuildingVersionGuardWritesActiveOnlyForStaleBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "news_view",
		PrimaryDatasetId:    "news",
		DatasetIds:          []string{"news"},
		Engine:              "bleve",
		Status:              "active",
		BuildStatus:         "building",
		ViewVersion:         2,
		ActiveResult:        "a",
		ActiveViewVersion:   1,
		BuildingResult:      "b",
		BuildingViewVersion: 1,
	})

	assertWriteTargets(t, writes, map[string]int{"a": 1})
}

func TestRecordBuildingStatusGuardWritesActiveOnlyForFailedBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "news_view",
		PrimaryDatasetId:    "news",
		DatasetIds:          []string{"news"},
		Engine:              "bleve",
		Status:              "active",
		BuildStatus:         "failed",
		ViewVersion:         2,
		ActiveResult:        "a",
		ActiveViewVersion:   1,
		BuildingResult:      "b",
		BuildingViewVersion: 2,
	})

	assertWriteTargets(t, writes, map[string]int{"a": 1})
}

func TestRecordCurrentBuildingWritesActiveAndBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "news_view",
		PrimaryDatasetId:    "news",
		DatasetIds:          []string{"news"},
		Engine:              "bleve",
		Status:              "active",
		BuildStatus:         "building",
		ViewVersion:         2,
		ActiveResult:        "a",
		ActiveViewVersion:   1,
		BuildingResult:      "b",
		BuildingViewVersion: 2,
	})

	assertWriteTargets(t, writes, map[string]int{"a": 1, "b": 1})
}

func runRecordBuildingWriteTest(t *testing.T, view *pb.View) map[string]int {
	t.Helper()
	ctx := context.Background()
	key := &pb.RecordKey{
		SpaceId:   view.GetSpaceId(),
		DatasetId: view.GetPrimaryDatasetId(),
		RecordId:  "news-1",
		Version:   "2026-07-09T01:00:00Z",
	}
	indexer := &recordingRecordIndexer{writes: map[string]int{}}
	service := &Service{
		reader: &buildingGuardReader{
			recordRows: []*pb.RecordRow{testBuilderRecordRow(key)},
		},
		metadata: newBuildingGuardMetadata(view),
		search:   indexer,
	}
	if err := service.processRecordBatch(ctx, []*pb.RecordKey{key}); err != nil {
		t.Fatalf("processRecordBatch: %v", err)
	}
	return indexer.writes
}

type recordingRecordIndexer struct {
	writes map[string]int
}

func (w *recordingRecordIndexer) IndexRecordViewRows(_ context.Context, resultName string, _ []*pb.ViewColumn, rows []*pb.RecordRow) error {
	if w.writes == nil {
		w.writes = map[string]int{}
	}
	w.writes[resultName] += len(rows)
	return nil
}

func testBuilderRecordRow(key *pb.RecordKey) *pb.RecordRow {
	return &pb.RecordRow{
		Key: proto.Clone(key).(*pb.RecordKey),
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.23}},
		}},
	}
}
