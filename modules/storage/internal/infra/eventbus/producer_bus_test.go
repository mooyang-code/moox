package eventbus

import (
	"context"
	"errors"
	"testing"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
)

func TestSubscriberBusInvokesAllTimeSeriesHandlers(t *testing.T) {
	firstErr := errors.New("first projection failed")
	secondErr := errors.New("second projection failed")
	secondCalled := false
	first, err := proto.Marshal(&pb.TimeSeriesRowsUpdated{MessageId: "event-1"})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	bus := &SubscriberBus{
		timeSeriesHandlers: map[uint64]coreeventbus.TimeSeriesRowsUpdatedHandler{
			1: func(context.Context, *pb.TimeSeriesRowsUpdated) error { return firstErr },
			2: func(context.Context, *pb.TimeSeriesRowsUpdated) error {
				secondCalled = true
				return secondErr
			},
		},
	}

	err = bus.dispatch(context.Background(), &jetstream.Delivery{Message: &messagepb.MooxMessage{Payload: first}}, true)
	if !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
		t.Fatalf("handler error = %v, want both handler errors", err)
	}
	if !secondCalled {
		t.Fatal("second handler was not called after the first handler failed")
	}
}
