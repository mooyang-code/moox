package view

import (
	"context"
	"errors"
	"sync"
	"time"
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

func (s *Service) acquireLiveDelivery(ctx context.Context) error {
	s.initLiveGate()
	return s.liveGate.acquireRead(ctx)
}

func (s *Service) releaseLiveDelivery() {
	s.initLiveGate()
	s.liveGate.releaseRead()
}

func (s *Service) releaseBackfill() {
	s.initLiveGate()
	s.liveGate.releaseWrite()
}

// acquireActivationFence waits until the durable has no server-side backlog,
// then excludes live deliveries while metadata and the in-memory active index
// switch together. When the old physical active index is already unavailable,
// a READY replacement is the recovery authority; waiting for the pending row
// would deadlock activation, so the live lease itself becomes the fence.
func (s *Service) acquireActivationFence(ctx context.Context, spaceID, viewID, buildID string) error {
	s.mu.RLock()
	state := s.consumerState
	s.mu.RUnlock()
	bypassConsumerFence, err := s.replacementCanBypassConsumerFence(ctx, spaceID, viewID, buildID)
	if err != nil {
		return err
	}
	if state != nil && !bypassConsumerFence {
		for {
			consumerState, err := state(ctx)
			if err == nil && consumerState.NumPending == 0 && consumerState.NumAckPending == 0 {
				break
			}
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				if err != nil {
					return errors.Join(ctx.Err(), err)
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return s.acquireBackfill(ctx)
}

func (s *Service) replacementCanBypassConsumerFence(ctx context.Context, spaceID, viewID, buildID string) (bool, error) {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return false, nil
	}
	runtime.mu.Lock()
	activeID, nextID := runtime.active, runtime.next
	runtime.mu.Unlock()
	if nextID == "" || (buildID != "" && nextID != buildID) {
		return false, nil
	}
	nextEngine, err := s.engineFor(nextID)
	if err != nil {
		return false, nil
	}
	nextStats, err := nextEngine.Stat(ctx, nextID)
	if err != nil || !nextStats.Exists {
		return false, nil
	}
	if activeID == "" {
		return true, nil
	}
	activeEngine, err := s.engineFor(activeID)
	if err != nil {
		return true, nil
	}
	activeStats, err := activeEngine.Stat(ctx, activeID)
	return err != nil || !activeStats.Exists, nil
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

func (g *liveLeaseGate) acquireRead(ctx context.Context) error {
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
