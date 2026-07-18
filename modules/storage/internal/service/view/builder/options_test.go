package builder

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestViewIndexBatchUsesWarmingBuildSchema(t *testing.T) {
	item := &pb.View{
		SpaceId: "crypto", ViewId: "news_view", Engine: "bleve",
		ActiveViewVersion: 1, ActiveSchemaHash: "active-hash",
		IndexBuild: &pb.ViewIndexBuild{TargetViewVersion: 2, SchemaHash: "build-hash"},
	}
	columns := []*pb.ViewColumn{{ColumnName: "title"}}

	batch := viewIndexBatch(item, columns, nil, []*pb.RecordRow{{}}, true)

	assert.Equal(t, uint64(2), batch.ViewVersion)
	assert.Equal(t, "build-hash", batch.SchemaHash)
}

func TestServiceEnqueueRecordProcessesBatch(t *testing.T) {
	bus := eventbus.NewMemoryBus()
	recordKey := &pb.RecordKey{
		SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "2026-07-10T12:00:00Z",
	}
	view := &pb.View{
		SpaceId: "crypto", ViewId: "news_view", PrimaryDatasetId: "news", DatasetIds: []string{"news"},
		Engine: "bleve", Status: "active", ViewVersion: 1,
		ActiveIndexId: builderIndexID("news_view", viewindex.SlotA),
		ActiveColumns: []*pb.ViewColumn{{
			ColumnName: "title", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "news.title",
		}},
	}
	writer := newRecordingViewIndexEngine("bleve")
	svc := NewService(Options{
		Events:     bus,
		Reader:     &buildingGuardReader{recordRows: []*pb.RecordRow{{Key: recordKey}}},
		Metadata:   newBuildingGuardMetadata(view),
		Engines:    map[string]viewindex.ViewIndexEngine{"bleve": writer},
		BatchSize:  1,
		BatchWait:  20 * time.Millisecond,
		MaxWorkers: 1,
	})
	ctx := context.Background()
	require.NoError(t, svc.Start(ctx))

	require.NoError(t, bus.PublishRecordRowsCommitted(ctx, &pb.RecordRowsCommitted{
		Writes: []*pb.RecordRowWrite{{Operation: pb.RowWriteOperation_ROW_WRITE_OPERATION_MERGE, Row: &pb.RecordRow{Key: recordKey}}},
	}))
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, svc.Close())

	assert.NotEmpty(t, writer.writes)
}

func TestMissingAccessReaderReturnsError(t *testing.T) {
	reader := missingAccessReader{}
	_, err := reader.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{})
	require.Error(t, err)
	_, _, err = reader.ScanRecordRows(context.Background(), "crypto", "news", nil, nil, nil)
	require.Error(t, err)
}
