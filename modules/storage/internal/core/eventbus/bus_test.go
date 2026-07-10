package eventbus

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMemoryBusReturnsSubscriberErrors(t *testing.T) {
	bus := NewMemoryBus()
	want := errors.New("projection failed")
	if _, err := bus.SubscribeTimeSeriesRowsChanged(context.Background(), func(context.Context, *pb.TimeSeriesRowsChangedEvent) error {
		return want
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsChanged: %v", err)
	}

	err := bus.PublishTimeSeriesRowsChanged(context.Background(), &pb.TimeSeriesRowsChangedEvent{})
	if !errors.Is(err, want) {
		t.Fatalf("PublishTimeSeriesRowsChanged error = %v, want subscriber error", err)
	}
}

func TestMemoryBusAppliesBackpressure(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := bus.SubscribeRecordRowsChanged(context.Background(), func(context.Context, *pb.RecordRowsChangedEvent) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeRecordRowsChanged: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- bus.PublishRecordRowsChanged(context.Background(), &pb.RecordRowsChangedEvent{})
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("publish returned before subscriber completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("PublishRecordRowsChanged: %v", err)
	}
}

func TestMemoryBusCloseWaitsForInFlightHandlers(t *testing.T) {
	bus := NewMemoryBus()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := bus.SubscribeTimeSeriesRowsChanged(context.Background(), func(context.Context, *pb.TimeSeriesRowsChangedEvent) error {
		close(started)
		<-release
		return nil
	}); err != nil {
		t.Fatalf("SubscribeTimeSeriesRowsChanged: %v", err)
	}

	publishDone := make(chan error, 1)
	go func() {
		publishDone <- bus.PublishTimeSeriesRowsChanged(context.Background(), &pb.TimeSeriesRowsChangedEvent{})
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
		t.Fatalf("PublishTimeSeriesRowsChanged: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}
