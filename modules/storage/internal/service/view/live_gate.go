package view

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/packages/jetstream"
)

func (s *Service) initLiveGate() {
	s.liveGateOnce.Do(func() {
		s.liveGate = newLiveLeaseGate()
	})
}

func (s *Service) acquireBackfill(ctx context.Context) error {
	s.initLiveGate()
	return s.liveGate.acquireWrite(ctx)
}

func (s *Service) acquireLiveDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	s.initLiveGate()
	return s.liveGate.acquireRead(ctx, delivery)
}

func (s *Service) releaseLiveDelivery() {
	s.initLiveGate()
	s.liveGate.releaseRead()
}

func (s *Service) releaseBackfill() {
	s.initLiveGate()
	s.liveGate.releaseWrite()
}

// releaseLiveGate remains as a compatibility shim for old live callers.
func (s *Service) releaseLiveGate() {
	s.releaseLiveDelivery()
}

type liveLeaseGate struct {
	mu             sync.Mutex
	readers        int
	writer         bool
	waitingWriters int
	notify         chan struct{}
}

func newLiveLeaseGate() *liveLeaseGate {
	return &liveLeaseGate{notify: make(chan struct{})}
}

func (g *liveLeaseGate) acquireRead(ctx context.Context, _ *jetstream.Delivery) error {
	if ctx == nil {
		return errors.New("storage view read lease context is required")
	}
	for {
		g.mu.Lock()
		if !g.writer && g.waitingWriters == 0 {
			g.readers++
			g.mu.Unlock()
			return nil
		}
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-notify:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (g *liveLeaseGate) releaseRead() {
	g.mu.Lock()
	if g.readers > 0 {
		g.readers--
	}
	if g.readers == 0 {
		g.signalLocked()
	}
	g.mu.Unlock()
}

func (g *liveLeaseGate) acquireWrite(ctx context.Context) error {
	if ctx == nil {
		return errors.New("storage view write lease context is required")
	}
	g.mu.Lock()
	g.waitingWriters++
	g.mu.Unlock()
	for {
		g.mu.Lock()
		if !g.writer && g.readers == 0 {
			g.writer = true
			g.waitingWriters--
			g.mu.Unlock()
			return nil
		}
		notify := g.notify
		g.mu.Unlock()
		select {
		case <-notify:
		case <-ctx.Done():
			g.mu.Lock()
			g.waitingWriters--
			g.signalLocked()
			g.mu.Unlock()
			return ctx.Err()
		}
	}
}

func (g *liveLeaseGate) releaseWrite() {
	g.mu.Lock()
	g.writer = false
	g.signalLocked()
	g.mu.Unlock()
}

func (g *liveLeaseGate) signalLocked() {
	close(g.notify)
	g.notify = make(chan struct{})
}
