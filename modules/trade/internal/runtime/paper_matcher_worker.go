package runtime

import (
	"context"
	"sync/atomic"
	"time"
)

type MatcherScanner interface{ Scan(context.Context) error }
type PaperMatcherWorker struct {
	Matcher   MatcherScanner
	Interval  time.Duration
	wake      chan struct{}
	ready     atomic.Bool
	lastError atomic.Value
}

func NewPaperMatcherWorker(m MatcherScanner, interval time.Duration) *PaperMatcherWorker {
	if interval <= 0 {
		interval = time.Second
	}
	return &PaperMatcherWorker{Matcher: m, Interval: interval, wake: make(chan struct{}, 1)}
}
func (w *PaperMatcherWorker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *PaperMatcherWorker) Ready() bool { return w != nil && w.ready.Load() }
func (w *PaperMatcherWorker) State() (bool, string) {
	if w == nil {
		return false, "paper matcher worker is not configured"
	}
	lastError, _ := w.lastError.Load().(string)
	return w.Ready(), lastError
}
func (w *PaperMatcherWorker) Run(ctx context.Context) error {
	if w == nil || w.Matcher == nil {
		return context.Canceled
	}
	w.ready.Store(false)
	defer w.ready.Store(false)
	ticker := time.NewTicker(w.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			w.scan(ctx)
		case <-w.wake:
			w.scan(ctx)
		}
	}
}

func (w *PaperMatcherWorker) scan(ctx context.Context) {
	if err := w.Matcher.Scan(ctx); err != nil {
		w.ready.Store(false)
		w.lastError.Store(err.Error())
		return
	}
	if err := ctx.Err(); err != nil {
		w.ready.Store(false)
		w.lastError.Store(err.Error())
		return
	}
	w.ready.Store(true)
	w.lastError.Store("")
}
