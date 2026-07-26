package scheduler

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"hash/fnv"
	"sync"
)

type Service struct {
	action  *action.Service
	queues  []chan domain.Task
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.RWMutex
	stopped bool
}

func New(a *action.Service, n int) *Service {
	if n < 1 {
		n = 1
	}
	return &Service{action: a, queues: make([]chan domain.Task, n)}
}
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx != nil && !s.stopped {
		return
	}
	s.stopped = false
	s.ctx, s.cancel = context.WithCancel(ctx)
	for i := range s.queues {
		s.queues[i] = make(chan domain.Task, 64)
		s.wg.Add(1)
		go s.loop(s.queues[i])
	}
}
func (s *Service) loop(q <-chan domain.Task) {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case t := <-q:
			d, err := s.action.Repo.GetDefinition(s.ctx, t.StrategyID, t.Version)
			if err != nil {
				continue
			}
			_, _, _ = s.action.Run(s.ctx, t, d)
		}
	}
}
func (s *Service) Enqueue(ctx context.Context, t domain.Task) error {
	s.mu.RLock()
	workerCtx := s.ctx
	stopped := s.stopped
	s.mu.RUnlock()
	if workerCtx == nil || stopped {
		return errors.New("strategy scheduler is not started")
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.BindingID))
	select {
	case s.queues[int(h.Sum32()%uint32(len(s.queues)))] <- t:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-workerCtx.Done():
		return workerCtx.Err()
	}
}
func (s *Service) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}
