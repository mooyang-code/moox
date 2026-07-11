package process

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Factory func(context.Context) (Worker, error)
type SupervisorConfig struct {
	BackoffMin, BackoffMax time.Duration
	MaxConsecutiveFailures int
	MaxRetries             int
}
type Supervisor struct {
	factory            Factory
	cfg                SupervisorConfig
	mu                 sync.Mutex
	runMu              sync.Mutex
	worker             Worker
	failures, restarts int
	failed             bool
}

func NewSupervisor(factory Factory, cfg SupervisorConfig) *Supervisor {
	if cfg.BackoffMin <= 0 {
		cfg.BackoffMin = 50 * time.Millisecond
	}
	if cfg.BackoffMax <= 0 {
		cfg.BackoffMax = 2 * time.Second
	}
	if cfg.MaxConsecutiveFailures <= 0 {
		cfg.MaxConsecutiveFailures = 5
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	return &Supervisor{factory: factory, cfg: cfg}
}
func (s *Supervisor) Ensure(ctx context.Context) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker != nil && s.worker.State() == StateReady {
		return s.worker, nil
	}
	if s.failed {
		return nil, errors.New("pyruntime: supervisor is in crash-loop failure")
	}
	w, err := s.factory(ctx)
	if err != nil {
		s.failures++
		if s.failures >= s.cfg.MaxConsecutiveFailures {
			s.failed = true
		}
		return nil, err
	}
	s.worker = w
	s.failures = 0
	return w, nil
}
func (s *Supervisor) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.runLocked(ctx, nil, req)
}

// RunLoaded serializes loading and execution on one resident worker. Keeping
// both operations under the same lock prevents a concurrent request from
// replacing a busy worker or changing the module selected for this request.
func (s *Supervisor) RunLoaded(ctx context.Context, load LoadRequest, req RunRequest) (RunResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.runLocked(ctx, &load, req)
}

func (s *Supervisor) RunLoadedMany(ctx context.Context, loads []LoadRequest, req RunRequest) (RunResult, error) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	w, err := s.Ensure(ctx)
	if err != nil {
		return RunResult{}, err
	}
	for _, load := range loads {
		if err := w.Load(ctx, load); err != nil {
			return RunResult{}, s.restart(w, err)
		}
	}
	return w.Run(ctx, req)
}

func (s *Supervisor) Load(ctx context.Context, req LoadRequest) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	w, err := s.Ensure(ctx)
	if err != nil {
		return err
	}
	if err := w.Load(ctx, req); err != nil {
		return s.restart(w, err)
	}
	return nil
}

func (s *Supervisor) runLocked(ctx context.Context, load *LoadRequest, req RunRequest) (RunResult, error) {
	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		w, err := s.Ensure(ctx)
		if err == nil && load != nil {
			err = w.Load(ctx, *load)
		}
		if err == nil {
			var result RunResult
			result, err = w.Run(ctx, req)
			if err == nil {
				return result, nil
			}
		}
		lastErr = err
		if attempt == s.cfg.MaxRetries {
			break
		}
		_ = s.restart(w, err)
		if err := waitBackoff(ctx, s.cfg.BackoffMin, s.cfg.BackoffMax, attempt); err != nil {
			return RunResult{}, err
		}
	}
	return RunResult{}, lastErr
}

func (s *Supervisor) restart(w Worker, cause error) error {
	if w == nil {
		return cause
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker != w {
		return cause
	}
	closeErr := w.Close()
	s.worker = nil
	s.restarts++
	if closeErr != nil {
		return errors.Join(cause, closeErr)
	}
	return cause
}

func waitBackoff(ctx context.Context, min, max time.Duration, attempt int) error {
	d := min
	for i := 0; i < attempt; i++ {
		if d >= max/2 {
			d = max
			break
		}
		d *= 2
	}
	if d > max {
		d = max
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (s *Supervisor) Restarts() int { s.mu.Lock(); defer s.mu.Unlock(); return s.restarts }
func (s *Supervisor) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil {
		if s.failed {
			return StateDead
		}
		return StateStarting
	}
	return s.worker.State()
}
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil {
		return nil
	}
	err := s.worker.Close()
	s.worker = nil
	return err
}
