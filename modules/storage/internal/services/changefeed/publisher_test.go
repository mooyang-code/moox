package changefeed_test

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/services/changefeed"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestMemoryPublisherRecordsRowsChangedEvent(t *testing.T) {
	ctx := context.Background()
	publisher := changefeed.NewMemoryPublisher()

	err := publisher.PublishRowsChanged(ctx, &pb.DataRowsChangedEvent{
		Scope:     &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"},
		EventTime: "2026-06-15T00:00:00+08:00",
		Rows:      []*pb.DataRow{{Key: &pb.DataKey{DataTime: "2026-06-15T00:00:00+08:00"}}},
	})

	require.NoError(t, err)
	require.Len(t, publisher.Events(), 1)
}
