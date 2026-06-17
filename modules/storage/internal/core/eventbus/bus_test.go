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
