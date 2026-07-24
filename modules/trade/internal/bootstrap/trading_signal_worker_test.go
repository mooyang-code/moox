package bootstrap

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/events/tradingpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestTradingSignalRecordPreservesOptionalPricesAndTags(t *testing.T) {
	target, stop, take := 101.5, 98.25, 105.75
	record, err := tradingSignalRecord(
		&eventpb.EventMessage{EventId: "event-1", SpaceId: "space-1", OccurredAt: timestamppb.New(time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC))},
		&tradingpb.TradingSignal{
			StrategyId: "strategy-1", SignalId: "signal-1", Symbol: "BTC-USDT",
			Side: tradingpb.SignalSide_SIGNAL_SIDE_BUY, Action: tradingpb.SignalAction_SIGNAL_ACTION_OPEN,
			TargetPrice: &target, StopLossPrice: &stop, TakeProfitPrice: &take,
			SignalTime: timestamppb.New(time.Date(2026, 7, 23, 9, 59, 0, 0, time.UTC)),
			Tags:       map[string]string{"source": "factor"},
		},
		time.Date(2026, 7, 23, 10, 0, 2, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if record.TargetPrice != "101.5" || record.StopLossPrice != "98.25" || record.TakeProfitPrice != "105.75" {
		t.Fatalf("optional prices = %q/%q/%q", record.TargetPrice, record.StopLossPrice, record.TakeProfitPrice)
	}
	if record.Tags != `{"source":"factor"}` {
		t.Fatalf("tags = %q", record.Tags)
	}
	if !record.SignalTime.Equal(time.Date(2026, 7, 23, 9, 59, 0, 0, time.UTC)) {
		t.Fatalf("signal time = %v", record.SignalTime)
	}
}
