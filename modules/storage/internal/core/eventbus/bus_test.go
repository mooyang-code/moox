package eventbus

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

func TestMemoryBusPublishesEveryCommittedRecordRevision(t *testing.T) {
	bus := NewMemoryBus()
	var revisions []uint64
	if _, err := bus.SubscribeRecordRowsCommitted(context.Background(), func(_ context.Context, event *pb.RecordRowsCommittedEvent) error {
		for _, row := range event.GetRows() {
			revisions = append(revisions, row.GetRevision())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for revision := uint64(1); revision <= 2; revision++ {
		if err := bus.PublishRecordRowsCommitted(context.Background(), &pb.RecordRowsCommittedEvent{EventId: "source:" + fmt.Sprint(revision), Rows: []*pb.RecordRow{{Revision: revision}}}); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(revisions, []uint64{1, 2}) {
		t.Fatalf("revisions = %v", revisions)
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
