package builder

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestRecordBuildingVersionGuardWritesActiveOnlyForStaleBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "news_view",
		PrimaryDatasetId:  "news",
		DatasetIds:        []string{"news"},
		Engine:            "bleve",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("news_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("news_view", viewindex.SlotB), TargetViewVersion: 1, State: pb.ViewIndexBuild_BUILDING,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("news_view", viewindex.SlotA): 1})
}

func TestRecordBuildingStatusGuardWritesActiveOnlyForFailedBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "news_view",
		PrimaryDatasetId:  "news",
		DatasetIds:        []string{"news"},
		Engine:            "bleve",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("news_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("news_view", viewindex.SlotB), TargetViewVersion: 2, State: pb.ViewIndexBuild_FAILED,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("news_view", viewindex.SlotA): 1})
}

func TestRecordCurrentBuildingWritesActiveAndBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "news_view",
		PrimaryDatasetId:  "news",
		DatasetIds:        []string{"news"},
		Engine:            "bleve",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("news_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("news_view", viewindex.SlotB), TargetViewVersion: 2, State: pb.ViewIndexBuild_BUILDING,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("news_view", viewindex.SlotA): 1, builderIndexID("news_view", viewindex.SlotB): 1})
}

func TestRecordCurrentCatchingUpWritesActiveAndBuilding(t *testing.T) {
	writes := runRecordBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "news_view",
		PrimaryDatasetId:  "news",
		DatasetIds:        []string{"news"},
		Engine:            "bleve",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("news_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("news_view", viewindex.SlotB), TargetViewVersion: 2, State: pb.ViewIndexBuild_CATCHING_UP,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("news_view", viewindex.SlotA): 1, builderIndexID("news_view", viewindex.SlotB): 1})
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
	indexer := newRecordingViewIndexEngine("bleve")
	service := &Service{
		reader: &buildingGuardReader{
			recordRows: []*pb.RecordRow{testBuilderRecordRow(key)},
		},
		metadata: newBuildingGuardMetadata(view),
		engines:  map[string]viewindex.ViewIndexEngine{"bleve": indexer},
	}
	if err := service.processRecordBatch(ctx, []*pb.RecordKey{key}); err != nil {
		t.Fatalf("processRecordBatch: %v", err)
	}
	return indexer.writes
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
