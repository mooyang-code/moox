package eventbus

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMemoryBusPublishesTimeSeriesRowsUpdatedWithRows(t *testing.T) {
	ctx := context.Background()
	bus := NewMemoryBus()
	received := make(chan *pb.TimeSeriesRowsUpdated, 1)

	if _, err := bus.SubscribeTimeSeriesRowsUpdated(ctx, func(_ context.Context, event *pb.TimeSeriesRowsUpdated) error {
		received <- event
		return nil
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsUpdated: %v", err)
	}

	event := &pb.TimeSeriesRowsUpdated{
		MessageId: "msg-1",
		WrittenAt: "2026-07-08T10:00:00Z",
		SpaceId:   "crypto",
		DatasetId: "kline",
		Rows: []*pb.TimeSeriesRow{{
			Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-08T10:00:00Z"},
		}},
	}
	if err := bus.PublishTimeSeriesRowsUpdated(ctx, event); err != nil {
		t.Fatalf("PublishTimeSeriesRowsUpdated: %v", err)
	}
	if err := bus.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	got := <-received
	if got.GetMessageId() != "msg-1" || got.GetSpaceId() != "crypto" || got.GetDatasetId() != "kline" {
		t.Fatalf("received event metadata = %+v, want message_id/space_id/dataset_id", got)
	}
	if len(got.GetRows()) != 1 || got.GetRows()[0].GetKey().GetSubjectId() != "BTC-USDT" {
		t.Fatalf("received rows = %+v, want original row payload", got.GetRows())
	}
	if len(bus.TimeSeriesEvents()) != 1 || len(bus.TimeSeriesEvents()[0].GetRows()) != 1 {
		t.Fatalf("stored events = %+v, want one rows-updated journal", bus.TimeSeriesEvents())
	}
}
