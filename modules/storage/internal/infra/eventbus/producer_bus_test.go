package eventbus

import (
	"context"
	"errors"
	"testing"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/transport"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSubscriberBusInvokesAllTimeSeriesHandlers(t *testing.T) {
	firstErr := errors.New("first projection failed")
	secondErr := errors.New("second projection failed")
	secondCalled := false
	first, err := protojson.Marshal(&pb.TimeSeriesRowsChangedEvent{EventId: "event-1"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	bus := &SubscriberBus{
		timeSeriesHandlers: map[uint64]coreeventbus.TimeSeriesRowsChangedHandler{
			1: func(context.Context, *pb.TimeSeriesRowsChangedEvent) error { return firstErr },
			2: func(context.Context, *pb.TimeSeriesRowsChangedEvent) error {
				secondCalled = true
				return secondErr
			},
		},
	}

	err = bus.handleTimeSeriesMessage(context.Background(), &transport.Message{Data: first})
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("handler error = %v, want both handler errors", err)
	}
	if !secondCalled {
		t.Fatal("second handler was not called after the first handler failed")
	}
}
