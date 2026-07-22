//go:build cgo

package view

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
)

func TestEventConsumerOptionsDefaults(t *testing.T) {
	opts, err := (EventConsumerOptions{}).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if opts.Stream != "MOOX_STORAGE" || opts.Durable != "storage_view" || opts.FetchBatch != 8 || opts.MaxWorkers != 4 || opts.Ordering != "subject" {
		t.Fatalf("options = %+v", opts)
	}
}

func TestSubjectLaneDifferentSubjectsRunInParallel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aStarted, bStarted, releaseA := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := newSubjectLaneDispatcher(ctx, 2, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.Subject == "A" {
			close(aStarted)
			<-releaseA
		} else {
			close(bStarted)
		}
		return nil
	}, nil)
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "B"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aStarted:
	case <-time.After(time.Second):
		t.Fatal("subject A did not start")
	}
	select {
	case <-bStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("subject B was blocked by subject A")
	}
	close(releaseA)
}

func TestSubjectLanePreservesFetchOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstStarted, releaseFirst, secondDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	d := newSubjectLaneDispatcher(ctx, 4, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		if delivery.RawMessageID == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		mu.Lock()
		order = append(order, delivery.RawMessageID)
		mu.Unlock()
		if delivery.RawMessageID == "second" {
			close(secondDone)
		}
		return nil
	}, nil)
	defer d.Close()
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "second"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	select {
	case <-secondDone:
		t.Fatal("same subject overtook an active delivery")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second delivery did not run")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("order = %v", order)
	}
}

func TestSubjectLaneRetryKeepsLaneBlockedButOtherSubjectRuns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	aFailed, retryA, aSecond, bStarted := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	d := newSubjectLaneDispatcher(ctx, 2, func(_ context.Context, delivery *jetstream.Delivery, _ *deliveryHeartbeat) error {
		switch delivery.RawMessageID {
		case "a1":
			close(aFailed)
			<-retryA // processDelivery holds the pending delivery during retry.
		case "a2":
			close(aSecond)
		case "b1":
			close(bStarted)
		}
		return nil
	}, nil)
	defer d.Close()
	for _, delivery := range []*jetstream.Delivery{{Subject: "A", RawMessageID: "a1"}, {Subject: "A", RawMessageID: "a2"}, {Subject: "B", RawMessageID: "b1"}} {
		if err := d.Dispatch(delivery); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-aFailed:
	case <-time.After(time.Second):
		t.Fatal("failed delivery did not start")
	}
	select {
	case <-bStarted:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("other subject was blocked by retry")
	}
	select {
	case <-aSecond:
		t.Fatal("same subject overtook retry")
	case <-time.After(50 * time.Millisecond):
	}
	close(retryA)
	select {
	case <-aSecond:
	case <-time.After(time.Second):
		t.Fatal("same subject did not resume")
	}
}

func TestSubjectLaneStartsHeartbeatWhenDeliveryIsQueuedAndStopsItOnClose(t *testing.T) {
	ctx := context.Background()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	queuedHeartbeat := make(chan *deliveryHeartbeat, 2)
	d := newSubjectLaneDispatcher(ctx, 1, func(handlerCtx context.Context, delivery *jetstream.Delivery, heartbeat *deliveryHeartbeat) error {
		if delivery.RawMessageID == "first" {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-handlerCtx.Done():
			}
		}
		if delivery.RawMessageID == "second" {
			t.Fatalf("queued delivery started before first finished")
		}
		if heartbeat == nil {
			t.Fatal("handler did not receive queued heartbeat")
		}
		return nil
	}, nil, laneMetricsHooks{
		newHeartbeat: func(context.Context, *jetstream.Delivery) *deliveryHeartbeat {
			h := &deliveryHeartbeat{stopCh: make(chan struct{}), doneCh: make(chan struct{})}
			go func() {
				<-h.stopCh
				close(h.doneCh)
			}()
			queuedHeartbeat <- h
			return h
		},
	})
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "first"}); err != nil {
		t.Fatal(err)
	}
	<-queuedHeartbeat // the first delivery also starts its heartbeat at dispatch.
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first delivery did not start")
	}
	if err := d.Dispatch(&jetstream.Delivery{Subject: "same", RawMessageID: "second"}); err != nil {
		t.Fatal(err)
	}
	var heartbeat *deliveryHeartbeat
	select {
	case heartbeat = <-queuedHeartbeat:
	case <-time.After(time.Second):
		t.Fatal("queued delivery did not start its heartbeat")
	}
	d.Close()
	select {
	case <-heartbeat.doneCh:
	case <-time.After(time.Second):
		t.Fatal("queued heartbeat was not stopped on dispatcher close")
	}
	close(releaseFirst)
}

func TestBackfillWriterWaitsForLiveAndBlocksNewLive(t *testing.T) {
	svc := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	liveStarted, releaseLive := make(chan struct{}), make(chan struct{})
	go func() {
		if err := svc.acquireLiveDelivery(ctx, nil); err == nil {
			close(liveStarted)
			<-releaseLive
			svc.releaseLiveDelivery()
		}
	}()
	select {
	case <-liveStarted:
	case <-time.After(time.Second):
		t.Fatal("live lease was not acquired")
	}
	writerStarted := make(chan struct{})
	go func() {
		if err := svc.acquireBackfill(ctx); err == nil {
			close(writerStarted)
		}
	}()
	select {
	case <-writerStarted:
		t.Fatal("backfill entered before live drained")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseLive)
	select {
	case <-writerStarted:
	case <-time.After(time.Second):
		t.Fatal("backfill did not acquire")
	}
	newLive := make(chan struct{})
	go func() {
		if err := svc.acquireLiveDelivery(ctx, nil); err == nil {
			close(newLive)
		}
	}()
	select {
	case <-newLive:
		t.Fatal("new live work entered during backfill")
	case <-time.After(50 * time.Millisecond):
	}
	svc.releaseBackfill()
	select {
	case <-newLive:
		svc.releaseLiveDelivery()
	case <-time.After(time.Second):
		t.Fatal("live work did not resume")
	}
}
