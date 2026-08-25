package runtime

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type AccountSampler interface {
	SampleAccount(context.Context, string) error
}
type AccountSamplerLister interface {
	ListSampleAccounts(context.Context) ([]string, error)
}
type EquitySampler struct {
	Service   AccountSampler
	Interval  time.Duration
	signal    chan struct{}
	mu        sync.Mutex
	pending   map[string]struct{}
	degraded  atomic.Bool
	lastError atomic.Value
}

func NewEquitySampler(service AccountSampler) *EquitySampler {
	return &EquitySampler{Service: service, Interval: time.Minute, signal: make(chan struct{}, 1), pending: map[string]struct{}{}}
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
func (s *EquitySampler) Run(ctx context.Context) error {
	interval := s.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.signal:
			_ = s.RunPending(ctx)
		case <-ticker.C:
			if lister, ok := s.Service.(AccountSamplerLister); ok {
				if ids, err := lister.ListSampleAccounts(ctx); err == nil {
					for _, id := range ids {
						s.Enqueue(id)
					}
				}
			}
			_ = s.RunPending(ctx)
		}
	}
}
func (s *EquitySampler) Degraded() bool { return s != nil && s.degraded.Load() }
