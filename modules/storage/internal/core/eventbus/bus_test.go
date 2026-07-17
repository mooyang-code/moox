package eventbus

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestMemoryBusReturnsSubscriberErrors(t *testing.T) {
	bus := NewMemoryBus()
	want := errors.New("projection failed")
	if _, err := bus.SubscribeTimeSeriesRowsUpdated(context.Background(), func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		return want
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsUpdated: %v", err)
	}

	err := bus.PublishTimeSeriesRowsUpdated(context.Background(), &pb.TimeSeriesRowsUpdated{})
	if !errors.Is(err, want) {
		t.Fatalf("PublishTimeSeriesRowsUpdated error = %v, want subscriber error", err)
	}
}

func TestMemoryBusAppliesBackpressure(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := bus.SubscribeRecordRowsUpdated(context.Background(), func(context.Context, *pb.RecordRowsUpdated) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeRecordRowsUpdated: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- bus.PublishRecordRowsUpdated(context.Background(), &pb.RecordRowsUpdated{})
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("publish returned before subscriber completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("PublishRecordRowsUpdated: %v", err)
	}
}

func TestMemoryBusCloseWaitsForInFlightHandlers(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := bus.SubscribeTimeSeriesRowsUpdated(context.Background(), func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsUpdated: %v", err)
	}

	publishDone := make(chan error, 1)
	go func() {
		publishDone <- bus.PublishTimeSeriesRowsUpdated(context.Background(), &pb.TimeSeriesRowsUpdated{})
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
		t.Fatalf("PublishTimeSeriesRowsUpdated: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMemorySubscriptionCloseWaitsOnlyForItsInFlightHandler(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	subscription, err := bus.SubscribeTimeSeriesRowsUpdated(context.Background(), func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bus.SubscribeTimeSeriesRowsUpdated(context.Background(), func(context.Context, *pb.TimeSeriesRowsUpdated) error { return nil }); err != nil {
		t.Fatal(err)
	}
	publishDone := make(chan error, 1)
	go func() {
		publishDone <- bus.PublishTimeSeriesRowsUpdated(context.Background(), &pb.TimeSeriesRowsUpdated{})
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
	subscription, err := bus.SubscribeRecordRowsUpdated(context.Background(), func(context.Context, *pb.RecordRowsUpdated) error {
		panic("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = bus.PublishRecordRowsUpdated(context.Background(), &pb.RecordRowsUpdated{})
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
