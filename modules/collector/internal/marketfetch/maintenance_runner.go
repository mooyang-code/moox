package marketfetch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultMaintenanceInterval = time.Minute
	DefaultMaintenanceTimeout  = 45 * time.Second
)

// MaintenanceRunner serializes process-level cleanup and reconciliation work.
// Wakeups are coalesced so scheduler ticks never queue an unbounded backlog.
type MaintenanceRunner struct {
	Metrics  *Metrics
	interval time.Duration
	timeout  time.Duration
	pass     func(context.Context) error
	wake     chan struct{}
	startMu  sync.Mutex
	started  bool
}

func NewMaintenanceRunner(interval, timeout time.Duration, pass func(context.Context) error) *MaintenanceRunner {
	return &MaintenanceRunner{interval: interval, timeout: timeout, pass: pass, wake: make(chan struct{}, 1)}
}

func (r *MaintenanceRunner) Start(ctx context.Context) error {
	if r == nil || r.pass == nil {
		return fmt.Errorf("Collector maintenance runner is not initialized")
	}
	if r.interval <= 0 || r.timeout <= 0 || r.timeout > r.interval {
		return fmt.Errorf("Collector maintenance interval and timeout must be positive and timeout must not exceed interval")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if r.started {
		return fmt.Errorf("Collector maintenance runner already started")
	}
	r.started = true
	go r.run(ctx)
	return nil
}

func (r *MaintenanceRunner) Wake() {
	if r == nil || r.wake == nil {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *MaintenanceRunner) run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	var lastRun time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
		if !lastRun.IsZero() && time.Since(lastRun) < r.interval {
			continue
		}
		lastRun = time.Now()
		r.runPass(ctx)
	}
}

func (r *MaintenanceRunner) runPass(ctx context.Context) {
	start := time.Now()
	passCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	err := r.pass(passCtx)
	if err == nil {
		err = passCtx.Err()
	}
	r.Metrics.ObserveMaintenancePass(time.Since(start), err)
	if err != nil {
		log.WarnContextf(passCtx, "Collector maintenance pass failed: %v", err)
	}
}
