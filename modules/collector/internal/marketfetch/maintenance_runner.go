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
	DefaultMaintenanceOffset   = 35 * time.Second
	DefaultMaintenanceTimeout  = 20 * time.Second
)

// MaintenanceRunner serializes process-level cleanup and reconciliation work.
// Passes start at a fixed phase inside each interval (offset after the
// interval boundary) and must finish before the next boundary, so cleanup
// never shares the minute-boundary window with the market fetch tick that
// owns Collector's single SQLite connection at that moment.
type MaintenanceRunner struct {
	Metrics  *Metrics
	interval time.Duration
	offset   time.Duration
	timeout  time.Duration
	pass     func(context.Context) error
	now      func() time.Time
	startMu  sync.Mutex
	started  bool
}

func NewMaintenanceRunner(interval, offset, timeout time.Duration, pass func(context.Context) error) *MaintenanceRunner {
	return &MaintenanceRunner{interval: interval, offset: offset, timeout: timeout, pass: pass, now: time.Now}
}

// ValidateMaintenanceSchedule keeps every pass inside one interval: it starts
// offset after the boundary and its timeout ends before the next boundary.
func ValidateMaintenanceSchedule(interval, offset, timeout time.Duration) error {
	if interval <= 0 || timeout <= 0 || offset < 0 || offset >= interval || offset+timeout > interval {
		return fmt.Errorf("Collector maintenance requires positive interval and timeout, 0 <= offset < interval, and offset + timeout <= interval")
	}
	return nil
}

func (r *MaintenanceRunner) Start(ctx context.Context) error {
	_, err := r.StartWithDone(ctx)
	return err
}

func (r *MaintenanceRunner) StartWithDone(ctx context.Context) (<-chan struct{}, error) {
	if r == nil || r.pass == nil {
		return nil, fmt.Errorf("Collector maintenance runner is not initialized")
	}
	if err := ValidateMaintenanceSchedule(r.interval, r.offset, r.timeout); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if r.started {
		return nil, fmt.Errorf("Collector maintenance runner already started")
	}
	r.started = true
	done := make(chan struct{})
	go func() { defer close(done); r.run(ctx) }()
	return done, nil
}

// nextRun returns the first phase-aligned start strictly after now.
func (r *MaintenanceRunner) nextRun(now time.Time) time.Time {
	next := now.Truncate(r.interval).Add(r.offset)
	if !next.After(now) {
		next = next.Add(r.interval)
	}
	return next
}

func (r *MaintenanceRunner) run(ctx context.Context) {
	for {
		now := r.now()
		timer := time.NewTimer(r.nextRun(now).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
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
