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

	err := bus.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
		Scope:     &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"},
		EventTime: "2026-06-15T00:00:00+08:00",
		Rows:      []*pb.DataRow{{Key: &pb.DataKey{DataTime: "2026-06-15T00:00:00+08:00"}}},
	})

	require.NoError(t, err)
	require.Len(t, bus.Events(), 1)
}

func TestMemoryBusRowsChangedSubscriptionCanClose(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	var called int
	sub, err := bus.SubscribeRowsChanged(ctx, func(ctx context.Context, event *pb.DataRowsChangedEvent) error {
		_ = ctx
		_ = event
		called++
		return nil
	})
	require.NoError(t, err)

	err = bus.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
		EventId: "evt-1",
		Scope:   &pb.DataScope{SpaceId: "crypto", DatasetId: "kline"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)

	require.NoError(t, sub.Close())
	err = bus.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
		EventId: "evt-2",
		Scope:   &pb.DataScope{SpaceId: "crypto", DatasetId: "kline"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, called)
}
