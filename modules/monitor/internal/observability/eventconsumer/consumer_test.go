package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
)

func TestConsumerRequiresAllRoutes(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewConsumer(context.Background(), nil, registry, Config{}, Routes{
		Metrics: func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error { return nil },
	})
	if err == nil {
		t.Fatal("NewConsumer accepted incomplete routes")
	}
}

func TestPermanentRouteErrorTerms(t *testing.T) {
	consumer := &Consumer{routes: Routes{
		Metrics: func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error {
			return Permanent(errors.New("invalid snapshot"))
		},
	}}
	result := consumer.routeError(context.Background(), "metrics", Permanent(errors.New("invalid snapshot")), 1)
	if result.Decision != jetstream.TERM {
		t.Fatalf("decision = %v, want TERM", result.Decision)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	for delivery, want := range map[uint64]time.Duration{0: time.Second, 1: time.Second, 2: 5 * time.Second, 9: 15 * time.Second} {
		if got := retryDelay(delivery); got != want {
			t.Fatalf("retryDelay(%d) = %s, want %s", delivery, got, want)
		}
	}
}
