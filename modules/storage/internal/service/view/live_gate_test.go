package view

import (
	"context"
	"testing"
	"time"
)

func TestBackfillWriterWaitsForLiveAndBlocksNewLive(t *testing.T) {
	svc := &Service{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	liveStarted, releaseLive := make(chan struct{}), make(chan struct{})
	go func() {
		if err := svc.acquireLiveDelivery(ctx); err == nil {
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
		if err := svc.acquireLiveDelivery(ctx); err == nil {
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
