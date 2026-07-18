package eventconsumer

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestMemoryBusPublishesPersistedMessage(t *testing.T) {
	bus := NewMemoryBus()
	seen := make(chan string, 1)
	if _, err := bus.SubscribeRowsCommitted(context.Background(), func(_ context.Context, event *RowsCommittedEvent) error {
		seen <- event.TimeSeries.GetShardId()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := proto.Marshal(&pb.TimeSeriesRowsCommitted{ShardId: "shard-1", Sequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	data, err := proto.Marshal(testTimeSeriesMessage(payload))
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.PublishMessage(context.Background(), data); err != nil {
		t.Fatalf("PublishMessage: %v", err)
	}
	if got := <-seen; got != "shard-1" {
		t.Fatalf("published shard = %q", got)
	}
}

func TestMemoryBusRejectsPublishWithoutSubscribers(t *testing.T) {
	bus := NewMemoryBus()
	if err := bus.PublishTimeSeriesRowsCommitted(context.Background(), &pb.TimeSeriesRowsCommitted{}); !errors.Is(err, ErrNoSubscribers) {
		t.Fatalf("PublishTimeSeriesRowsCommitted error = %v, want ErrNoSubscribers", err)
	}
}

func TestMemoryBusReturnsSubscriberErrors(t *testing.T) {
	bus := NewMemoryBus()
	want := errors.New("projection failed")
	if _, err := bus.SubscribeTimeSeriesRowsCommitted(context.Background(), func(context.Context, *pb.TimeSeriesRowsCommitted) error {
		return want
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsCommitted: %v", err)
	}

	err := bus.PublishTimeSeriesRowsCommitted(context.Background(), &pb.TimeSeriesRowsCommitted{})
	if !errors.Is(err, want) {
		t.Fatalf("PublishTimeSeriesRowsCommitted error = %v, want subscriber error", err)
	}
}

func TestMemoryBusAppliesBackpressure(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := bus.SubscribeRecordRowsCommitted(context.Background(), func(context.Context, *pb.RecordRowsCommitted) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeRecordRowsCommitted: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- bus.PublishRecordRowsCommitted(context.Background(), &pb.RecordRowsCommitted{})
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("publish returned before subscriber completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("PublishRecordRowsCommitted: %v", err)
	}
}

func TestMemoryBusCloseWaitsForInFlightHandlers(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := bus.SubscribeTimeSeriesRowsCommitted(context.Background(), func(context.Context, *pb.TimeSeriesRowsCommitted) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsCommitted: %v", err)
	}

	publishDone := make(chan error, 1)
	go func() {
		publishDone <- bus.PublishTimeSeriesRowsCommitted(context.Background(), &pb.TimeSeriesRowsCommitted{})
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- bus.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before in-flight handler completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-publishDone; err != nil {
		t.Fatalf("PublishTimeSeriesRowsCommitted: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMemorySubscriptionCloseWaitsOnlyForItsInFlightHandler(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	subscription, err := bus.SubscribeTimeSeriesRowsCommitted(context.Background(), func(context.Context, *pb.TimeSeriesRowsCommitted) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.SubscribeTimeSeriesRowsCommitted(context.Background(), func(context.Context, *pb.TimeSeriesRowsCommitted) error { return nil }); err != nil {
		t.Fatal(err)
	}
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- bus.PublishTimeSeriesRowsCommitted(context.Background(), &pb.TimeSeriesRowsCommitted{})
	}()
	<-started
	closeDone := make(chan error, 1)
	go func() { closeDone <- subscription.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("subscription Close returned before its handler: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestMemorySubscriptionCloseDoesNotDeadlockAfterHandlerPanic(t *testing.T) {
	bus := NewMemoryBus()
	subscription, err := bus.SubscribeRecordRowsCommitted(context.Background(), func(context.Context, *pb.RecordRowsCommitted) error {
		panic("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = bus.PublishRecordRowsCommitted(context.Background(), &pb.RecordRowsCommitted{})
	}()
	done := make(chan error, 1)
	go func() { done <- subscription.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("subscription Close deadlocked after handler panic")
	}
}
