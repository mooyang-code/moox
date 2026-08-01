package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

func TestMarketFetchBatchCompletedValidationRequiresGovernedRoute(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	payload := &marketfetchpb.MarketFetchBatchCompleted{
		BatchId: "batch-1", ScheduleId: "schedule-1", BatchKind: "realtime", DatasetId: "bars", Frequency: "1m", NodeId: "node-1",
		PlannedCount: 1, SuccessCount: 1, Items: []*marketfetchpb.MarketFetchItemResult{{SubjectId: "BTC-USDT", Outcome: "success"}}, CompletedAt: timestamppb.New(time.Now().UTC()), Status: "succeeded",
	}
	encoded, err := registry.Encode(MarketFetchBatchCompleted, payload, PublishOptions{EventID: "batch-1", OccurredAt: time.Now().UTC(), SpaceID: "crypto", SubjectID: "bars"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ValidateMessage(encoded.Message); err != nil {
		t.Fatalf("valid market fetch completion rejected: %v", err)
	}
	encoded.Message.SubjectId = "bars/1m"
	if _, err := registry.ValidateMessage(encoded.Message); err == nil {
		t.Fatal("expected route mismatch to be rejected")
	}
}
