package builder

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewServiceNormalizesEngineNamesAndWorkers(t *testing.T) {
	engine := newRecordingViewIndexEngine("bleve")
	svc := NewService(Options{
		Engines:    map[string]viewindex.ViewIndexEngine{" BLEVE ": engine, "": nil},
		MaxWorkers: 0,
		BatchSize:  0,
		BatchWait:  0,
	})

	assert.Equal(t, 1, svc.maxWorkers)
	assert.Equal(t, 500, svc.batchOpts.BatchSize)
	assert.Equal(t, 200*time.Millisecond, svc.batchOpts.BatchWait)
	assert.Same(t, engine, svc.engines["bleve"])
	assert.Len(t, svc.engines, 1)
}

func TestServiceStartValidatesRequiredDependencies(t *testing.T) {
	engine := newRecordingViewIndexEngine("bleve")
	base := Options{
		Events:   eventbus.NewMemoryBus(),
		Reader:   &buildingGuardReader{},
		Metadata: newBuildingGuardMetadata(),
		Engines:  map[string]viewindex.ViewIndexEngine{"bleve": engine},
	}

	tests := []struct {
		name    string
		svc     *Service
		wantErr string
	}{
		{name: "nil service", svc: nil, wantErr: "nil"},
		{name: "missing events", svc: &Service{reader: base.Reader, metadata: base.Metadata, engines: base.Engines}, wantErr: "event bus"},
		{name: "missing reader", svc: &Service{events: base.Events, metadata: base.Metadata, engines: base.Engines}, wantErr: "fact reader"},
		{name: "missing metadata", svc: &Service{events: base.Events, reader: base.Reader, engines: base.Engines}, wantErr: "metadata client"},
		{name: "missing engines", svc: &Service{events: base.Events, reader: base.Reader, metadata: base.Metadata}, wantErr: "view index engines"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.svc.Start(context.Background())
			require.Error(t, err)
			assert.Contains(t, strings.ToLower(err.Error()), tt.wantErr)
		})
	}
}

func TestServiceStartSubscribeAndCloseRoundTrip(t *testing.T) {
	bus := eventbus.NewMemoryBus()
	svc := NewService(Options{
		Events:     bus,
		Reader:     &buildingGuardReader{},
		Metadata:   newBuildingGuardMetadata(),
		Engines:    map[string]viewindex.ViewIndexEngine{"bleve": newRecordingViewIndexEngine("bleve")},
		BatchSize:  1,
		BatchWait:  50 * time.Millisecond,
		MaxWorkers: 1,
	})
	ctx := context.Background()
	require.NoError(t, svc.Start(ctx))

	require.NoError(t, svc.enqueueTimeSeries(ctx, nil))
	require.NoError(t, svc.enqueueRecord(ctx, nil))

	key := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "orphan", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-09T01:00:00Z",
	}
	require.NoError(t, bus.PublishTimeSeriesRowsUpdated(ctx, &pb.TimeSeriesRowsUpdated{
		Rows: []*pb.TimeSeriesRow{testBuilderTimeSeriesRow(key)},
	}))

	require.NoError(t, svc.Close())
}

func TestServiceCloseIdempotentWhenNotStarted(t *testing.T) {
	svc := NewService(Options{})
	require.NoError(t, svc.Close())
}

func TestServiceStartRejectsDoubleStart(t *testing.T) {
	bus := eventbus.NewMemoryBus()
	svc := NewService(Options{
		Events:   bus,
		Reader:   &buildingGuardReader{},
		Metadata: newBuildingGuardMetadata(),
		Engines:  map[string]viewindex.ViewIndexEngine{"bleve": newRecordingViewIndexEngine("bleve")},
	})
	ctx := context.Background()
	require.NoError(t, svc.Start(ctx))
	err := svc.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
	require.NoError(t, svc.Close())
}

func TestReadRecordProjectionRowsReturnsRows(t *testing.T) {
	key := &pb.RecordKey{
		SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "2026-07-10T12:00:00Z",
	}
	svc := &Service{
		reader: &buildingGuardReader{recordRows: []*pb.RecordRow{{Key: key}}},
	}
	rows, err := svc.readRecordProjectionRows(context.Background(), []*pb.RecordKey{key})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "news-1", rows[0].GetKey().GetRecordId())
}

func TestProcessRecordItemBatchSkipsNilRows(t *testing.T) {
	svc := &Service{}
	svc.processRecordItemBatch(context.Background(), []recordDeriveItem{{row: nil}})
}

func TestRetInfoErrorAndWaitDeriveResults(t *testing.T) {
	assert.NoError(t, retInfoError(&pb.RetInfo{Code: pb.ErrorCode_SUCCESS}))
	assert.Error(t, retInfoError(&pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "boom"}))
	assert.NoError(t, retInfoError(nil))

	done := make(chan error, 1)
	completeDeriveItem(done, nil)
	assert.NoError(t, <-done)
	completeDeriveItem(nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitDeriveResults(ctx, []chan error{make(chan error)})
	assert.ErrorIs(t, err, context.Canceled)
}
