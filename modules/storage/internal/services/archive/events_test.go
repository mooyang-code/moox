package archive_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/services/archive"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestEventConsumerSubscribesToRowsChangedEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus := eventbus.NewMemoryBus()
	var timeSeriesEvents int32
	var recordEvents int32
	consumer := archive.NewEventConsumer(archive.EventConsumerOptions{
		Events: bus,
		HandleTimeSeries: func(context.Context, *pb.TimeSeriesRowsChangedEvent) error {
			atomic.AddInt32(&timeSeriesEvents, 1)
			return nil
		},
		HandleRecord: func(context.Context, *pb.RecordRowsChangedEvent) error {
			atomic.AddInt32(&recordEvents, 1)
			return nil
		},
	})

	require.NoError(t, consumer.Start(ctx))
	t.Cleanup(func() {
		require.NoError(t, consumer.Close())
	})

	require.NoError(t, bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "binance_spot_kline"}},
	}))
	require.NoError(t, bus.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT"}},
	}))
	require.NoError(t, bus.Wait(ctx))
	require.Equal(t, int32(1), atomic.LoadInt32(&timeSeriesEvents))
	require.Equal(t, int32(1), atomic.LoadInt32(&recordEvents))
}
