package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/marketpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type memoryWriter struct{ bars []aggregate.Bar }

func (w *memoryWriter) Write(_ context.Context, bar aggregate.Bar) error {
	w.bars = append(w.bars, bar)
	return nil
}

type flakyWriter struct {
	failures int
	bars     []aggregate.Bar
}

func (w *flakyWriter) Write(_ context.Context, bar aggregate.Bar) error {
	if w.failures > 0 {
		w.failures--
		return errors.New("temporary storage failure")
	}
	w.bars = append(w.bars, bar)
	return nil
}

func TestProcessorWritesOnlyClosedAggregate(t *testing.T) {
	aggregator, err := aggregate.New("1m", "2m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	writer := new(memoryWriter)
	processor, err := NewProcessor(aggregator, writer)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		start := base.Add(time.Duration(i) * time.Minute)
		delivery := &events.EventDelivery{Delivery: &jetstream.Delivery{}, Message: &events.EventMessage{EventId: string(rune('a' + i)), EventName: events.TickReceived.Name, EventVersion: events.TickReceived.Version, SpaceId: "crypto", SubjectId: "BTC-USDT"}, Payload: &marketpb.Tick{Symbol: "BTC-USDT", Price: 100, Quantity: 1, TradeTime: timestamppb.New(start.Add(10 * time.Second))}}
		if err := processor.Process(context.Background(), delivery); err != nil {
			t.Fatal(err)
		}
	}
	if len(writer.bars) != 1 || !writer.bars[0].Closed {
		t.Fatalf("written bars = %+v", writer.bars)
	}
}

func TestProcessorAcceptsTickPayload(t *testing.T) {
	aggregator, err := aggregate.New("1m", "2m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	writer := new(memoryWriter)
	processor, err := NewProcessor(aggregator, writer)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id string
		at time.Time
	}{
		{id: "tick-1", at: base.Add(10 * time.Second)},
		{id: "tick-2", at: base.Add(70 * time.Second)},
	} {
		delivery := &events.EventDelivery{Delivery: &jetstream.Delivery{}, Message: &events.EventMessage{EventId: item.id, EventName: events.TickReceived.Name, EventVersion: events.TickReceived.Version, SpaceId: "crypto", SubjectId: "BTC-USDT"}, Payload: &marketpb.Tick{Symbol: "BTC-USDT", Price: 100, Quantity: 1, TradeTime: timestamppb.New(item.at)}}
		if err := processor.Process(context.Background(), delivery); err != nil {
			t.Fatal(err)
		}
	}
	if len(writer.bars) != 1 || !writer.bars[0].Closed {
		t.Fatalf("tick bars = %+v", writer.bars)
	}
}

func TestProcessorRejectsKlineOutputAsInput(t *testing.T) {
	aggregator, err := aggregate.New("1m", "5m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(aggregator, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	delivery := &events.EventDelivery{
		Delivery: &jetstream.Delivery{},
		Message:  &events.EventMessage{EventId: "kline-output", EventName: events.MarketKlineClosed.Name, EventVersion: events.MarketKlineClosed.Version, SpaceId: "crypto", SubjectId: "BTC-USDT"},
		Payload:  &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "5m", WindowStart: timestamppb.New(base), WindowEnd: timestamppb.New(base.Add(5 * time.Minute))},
	}
	if err := processor.Process(context.Background(), delivery); err == nil {
		t.Fatal("expected a published KlineClosed event to be rejected as input")
	}
}

func TestProcessorRejectsTickWithWrongEventName(t *testing.T) {
	aggregator, err := aggregate.New("1m", "5m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewProcessor(aggregator, nil)
	if err != nil {
		t.Fatal(err)
	}
	delivery := &events.EventDelivery{
		Delivery: &jetstream.Delivery{},
		Message:  &events.EventMessage{EventId: "wrong-event-name", EventName: events.MarketKlineClosed.Name, EventVersion: events.MarketKlineClosed.Version, SpaceId: "crypto", SubjectId: "BTC-USDT"},
		Payload:  &marketpb.Tick{Symbol: "BTC-USDT", Price: 100, Quantity: 1, TradeTime: timestamppb.New(time.Date(2026, 7, 23, 10, 0, 10, 0, time.UTC))},
	}
	if err := processor.Process(context.Background(), delivery); err == nil {
		t.Fatal("expected a Tick with a non-TickReceived event name to be rejected")
	}
}

func TestProcessorRetriesClosedOutputAfterWriterFailure(t *testing.T) {
	aggregator, err := aggregate.New("1m", "2m", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	writer := &flakyWriter{failures: 1}
	processor, err := NewProcessor(aggregator, writer)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	delivery := func(id string, start time.Time) *events.EventDelivery {
		return &events.EventDelivery{Delivery: &jetstream.Delivery{}, Message: &events.EventMessage{EventId: id, EventName: events.TickReceived.Name, EventVersion: events.TickReceived.Version, SpaceId: "crypto", SubjectId: "BTC-USDT"}, Payload: &marketpb.Tick{Symbol: "BTC-USDT", Price: 100, Quantity: 1, TradeTime: timestamppb.New(start.Add(10 * time.Second))}}
	}
	if err := processor.Process(context.Background(), delivery("first", base)); err != nil {
		t.Fatal(err)
	}
	closed := delivery("second", base.Add(time.Minute))
	if err := processor.Process(context.Background(), closed); err == nil {
		t.Fatal("expected the first storage write to fail")
	}
	if err := processor.Process(context.Background(), closed); err != nil {
		t.Fatal(err)
	}
	if len(writer.bars) != 1 || writer.bars[0].Volume != 2 {
		t.Fatalf("retried bars = %+v", writer.bars)
	}
}
