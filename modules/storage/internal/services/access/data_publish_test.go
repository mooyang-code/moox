package access

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestPublishTimeSeriesRowsUpdatedGroupsRowsByDataset(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	service := &Service{events: bus}

	rows := []*pb.TimeSeriesRow{
		accessTestTimeSeriesRow("crypto", "kline", "BTC-USDT"),
		accessTestTimeSeriesRow("crypto", "kline", "ETH-USDT"),
		accessTestTimeSeriesRow("crypto", "funding", "BTC-USDT"),
	}

	if err := service.publishTimeSeriesRowsUpdated(ctx, rows); err != nil {
		t.Fatalf("publishTimeSeriesRowsUpdated: %v", err)
	}

	events := bus.TimeSeriesEvents()
	if len(events) != 2 {
		t.Fatalf("events = %d, want one journal per dataset", len(events))
	}
	byDataset := map[string]*pb.TimeSeriesRowsUpdated{}
	for _, event := range events {
		byDataset[event.GetDatasetId()] = event
		if event.GetMessageId() == "" {
			t.Fatalf("message_id is empty: %+v", event)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.GetWrittenAt()); err != nil {
			t.Fatalf("written_at = %q, want RFC3339Nano: %v", event.GetWrittenAt(), err)
		}
	}
	if got := len(byDataset["kline"].GetRows()); got != 2 {
		t.Fatalf("kline rows = %d, want 2", got)
	}
	if got := len(byDataset["funding"].GetRows()); got != 1 {
		t.Fatalf("funding rows = %d, want 1", got)
	}
}

func accessTestTimeSeriesRow(spaceID, datasetID, subjectID string) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			SubjectId: subjectID,
			Freq:      "1m",
			DataTime:  "2026-07-08T10:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}},
		}},
	}
}
