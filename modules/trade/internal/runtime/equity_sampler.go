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
	Service AccountSampler
	signal  chan struct{}
	mu      sync.Mutex
	// runSem 是容量 1 的信号量（等待期间可响应 context）：后台 Run 持有它做现货报价估值时，
	// 带期限的定时器调用不能无限排队，超时直接返回，待采样任务留在队列里由后台 worker 处理。
	runSem    chan struct{}
	runOnce   sync.Once
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
	s.runOnce.Do(func() {
		if s.runSem == nil {
			s.runSem = make(chan struct{}, 1)
		}
	})
	select {
	case s.runSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.runSem }()
	return s.runPending(ctx)
}

// requeue 把没来得及采样的账户放回队列（期限到了或被取消时），并唤醒后台 worker。
func (s *EquitySampler) requeue(ids []string) {
	for _, id := range ids {
		s.Enqueue(id)
	}
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
	for i, id := range ids {
		if ctx.Err() != nil {
			// 期限已到：剩余账户保留在队列里，不能随着本次过期调用被丢弃
			s.requeue(ids[i:])
			if first == nil {
				first = ctx.Err()
			}
			break
		}
		if err := s.Service.SampleAccount(ctx, id); err != nil {
			if ctx.Err() != nil {
				s.requeue([]string{id})
			}
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
