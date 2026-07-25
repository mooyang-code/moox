package taskrunner

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
)

type fakeQueueConsumer struct {
	results []fakeFetchResult
}

type fakeFetchResult struct {
	deliveries []*jetstream.Delivery
	err        error
}

func (f *fakeQueueConsumer) Fetch(context.Context, int) ([]*jetstream.Delivery, error) {
	if len(f.results) == 0 {
		return nil, nats.ErrTimeout
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.deliveries, result.err
}
func (*fakeQueueConsumer) Close() error    { return nil }
func (*fakeQueueConsumer) MaxDeliver() int { return 3 }

func TestRoundRobinConsumerDoesNotStarveBindings(t *testing.T) {
	first := &fakeQueueConsumer{results: []fakeFetchResult{
		{deliveries: []*jetstream.Delivery{{Consumer: "first-1"}}},
		{deliveries: []*jetstream.Delivery{{Consumer: "first-2"}}},
	}}
	second := &fakeQueueConsumer{results: []fakeFetchResult{
		{deliveries: []*jetstream.Delivery{{Consumer: "second-1"}}},
	}}
	consumer := &roundRobinConsumer{bindings: []queueBinding{{consumer: first}, {consumer: second}}}
	for index, want := range []string{"first-1", "second-1", "first-2"} {
		rows, err := consumer.Fetch(context.Background(), 1)
		if err != nil || len(rows) != 1 || rows[0].Consumer != want {
			t.Fatalf("fetch %d = %+v, %v; want %s", index, rows, err, want)
		}
	}
}

func TestRoundRobinConsumerReturnsDeliveryAndErrorTogether(t *testing.T) {
	wantErr := errors.New("fetch failed after delivery")
	binding := &fakeQueueConsumer{results: []fakeFetchResult{{
		deliveries: []*jetstream.Delivery{{Consumer: "one"}},
		err:        wantErr,
	}}}
	consumer := &roundRobinConsumer{bindings: []queueBinding{{consumer: binding}}}
	rows, err := consumer.Fetch(context.Background(), 1)
	if len(rows) != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("fetch = %+v, %v", rows, err)
	}
}

func TestRoundRobinConsumerClosesEmptyRound(t *testing.T) {
	consumer := &roundRobinConsumer{bindings: []queueBinding{
		{consumer: &fakeQueueConsumer{}},
		{consumer: &fakeQueueConsumer{}},
	}}
	rows, err := consumer.Fetch(context.Background(), 1)
	if len(rows) != 0 || !errors.Is(err, jetstream.ErrClosed) {
		t.Fatalf("fetch = %+v, %v", rows, err)
	}
}
