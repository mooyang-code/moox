package runtime

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

type AccountSampler interface {
	SampleAccount(context.Context, string) error
}
type AccountSamplerLister interface {
	ListSampleAccounts(context.Context) ([]string, error)
}
type EquitySampler struct {
	Service   AccountSampler
	signal    chan struct{}
	mu        sync.Mutex
	runMu     sync.Mutex
	pending   map[string]struct{}
	degraded  atomic.Bool
	lastError atomic.Value
}

func NewEquitySampler(service AccountSampler) *EquitySampler {
	return &EquitySampler{Service: service, signal: make(chan struct{}, 1), pending: map[string]struct{}{}}
}
func (s *EquitySampler) Enqueue(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	s.pending[id] = struct{}{}
	s.mu.Unlock()
	select {
	case s.signal <- struct{}{}:
	default:
	}
}
func (s *EquitySampler) RunPending(ctx context.Context) error {
	if s == nil || s.Service == nil {
		return errors.New("equity sampler: service is not configured")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.runPending(ctx)
}

func (s *EquitySampler) runPending(ctx context.Context) error {
	s.mu.Lock()
	ids := make([]string, 0, len(s.pending))
	for id := range s.pending {
		ids = append(ids, id)
	}
	s.pending = map[string]struct{}{}
	s.mu.Unlock()
	sort.Strings(ids)
	var first error
	for _, id := range ids {
		if err := s.Service.SampleAccount(ctx, id); err != nil {
			s.degraded.Store(true)
			s.lastError.Store(err.Error())
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// Handle is registered on the tRPC timer. Periodic sampling is timer-owned;
// the local worker below only drains immediate fill/readiness wakeups.
func (s *EquitySampler) Handle(ctx context.Context) error {
	if s == nil || s.Service == nil {
		return errors.New("equity sampler: service is not configured")
	}
	if lister, ok := s.Service.(AccountSamplerLister); ok {
		ids, err := lister.ListSampleAccounts(ctx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			s.Enqueue(id)
		}
	}
	return s.RunPending(ctx)
}

func (s *EquitySampler) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.signal:
			_ = s.RunPending(ctx)
		}
	}
}
func (s *EquitySampler) Degraded() bool { return s != nil && s.degraded.Load() }
