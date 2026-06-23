package eventbus_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

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
	var called atomic.Int32
	sub, err := bus.SubscribeTimeSeriesRowsChanged(ctx, func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error {
		_ = ctx
		_ = event
		called.Add(1)
		return nil
	})
	require.NoError(t, err)

	err = bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId: "evt-1",
		Keys:    []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return called.Load() == 1
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, sub.Close())
	err = bus.PublishTimeSeriesRowsChanged(ctx, &pb.TimeSeriesRowsChangedEvent{
		EventId: "evt-2",
		Keys:    []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"}},
	})
	require.NoError(t, err)
	require.NoError(t, bus.Wait(context.Background()))
	require.Equal(t, int32(1), called.Load())
}

func TestMemoryBusPublishDoesNotRunHandlerInline(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	started := make(chan struct{})
	block := make(chan struct{})

	_, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		_ = ctx
		_ = event
		close(started)
		<-block
		return nil
	})
	require.NoError(t, err)

	published := make(chan error, 1)
	go func() {
		published <- bus.PublishRecordRowsChanged(ctx, &pb.RecordRowsChangedEvent{
			EventId: "evt-1",
			Keys:    []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "APT-USDT"}},
		})
	}()

	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	select {
	case err := <-published:
		require.NoError(t, err)
	default:
		close(block)
		require.FailNow(t, "PublishRecordRowsChanged ran the handler inline")
	}

	close(block)
	require.NoError(t, bus.Wait(context.Background()))
}

func TestMemoryBusRecordHandlerDoesNotObserveCallerMutationAfterPublish(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	started := make(chan struct{})
	block := make(chan struct{})
	seen := make(chan string, 1)

	_, err := bus.SubscribeRecordRowsChanged(ctx, func(ctx context.Context, event *pb.RecordRowsChangedEvent) error {
		_ = ctx
		close(started)
		<-block
		seen <- event.Keys[0].RecordId
		return nil
	})
	require.NoError(t, err)

	event := &pb.RecordRowsChangedEvent{
		EventId: "evt-1",
		Keys:    []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "A"}},
	}
	require.NoError(t, bus.PublishRecordRowsChanged(ctx, event))

	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	event.Keys[0].RecordId = "B"
	close(block)
	require.NoError(t, bus.Wait(context.Background()))
	require.Equal(t, "A", <-seen)
}

func TestMemoryBusRecordHistoryIsCloned(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	event := &pb.RecordRowsChangedEvent{
		EventId: "evt-1",
		Keys:    []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "symbols", RecordId: "A"}},
	}
	require.NoError(t, bus.PublishRecordRowsChanged(ctx, event))

	event.Keys[0].RecordId = "B"
	firstRead := bus.RecordEvents()
	require.Equal(t, "A", firstRead[0].GetKeys()[0].GetRecordId())

	firstRead[0].Keys[0].RecordId = "C"
	secondRead := bus.RecordEvents()
	require.Equal(t, "A", secondRead[0].GetKeys()[0].GetRecordId())
}
