package eventbus_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestMemoryBusRecordsRowsChangedEvent(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()

	err := bus.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
		EventTime: "2026-06-15T00:00:00+08:00",
		Keys:      []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT"}},
	})

	require.NoError(t, err)
	require.Len(t, bus.RecordEvents(), 1)
}

func TestMemoryBusRowsChangedSubscriptionCanClose(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	var called int
	sub, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
		_ = ctx
		_ = event
		called++
		return nil
	})
	require.NoError(t, err)

	err = bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId: "evt-1",
		Keys:    []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)

	require.NoError(t, sub.Close())
	err = bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId: "evt-2",
		Keys:    []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)
}
