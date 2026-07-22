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
		delivery := &events.EventDelivery{Delivery: &jetstream.Delivery{}, Message: &events.EventMessage{EventId: string(rune('a' + i)), SpaceId: "crypto", SubjectId: "BTC-USDT"}, Payload: &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(time.Minute)), Open: 1, High: 2, Low: 1, Close: 2, Volume: 1}}
		if err := processor.Process(context.Background(), delivery); err != nil {
			t.Fatal(err)
		}
	}
	if len(writer.bars) != 1 || !writer.bars[0].Closed {
		t.Fatalf("written bars = %+v", writer.bars)
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
		return &events.EventDelivery{Delivery: &jetstream.Delivery{}, Message: &events.EventMessage{EventId: id, SpaceId: "crypto", SubjectId: "BTC-USDT"}, Payload: &marketpb.KlineClosed{Symbol: "BTC-USDT", Frequency: "1m", WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(start.Add(time.Minute)), Open: 1, High: 2, Low: 1, Close: 2, Volume: 1}}
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
