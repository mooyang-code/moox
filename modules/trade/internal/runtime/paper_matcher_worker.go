package runtime

import (
	"context"
	"sync"
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

	subsMu sync.Mutex
	subs   map[chan struct{}]struct{}
}

func NewPaperMatcherWorker(m MatcherScanner, interval time.Duration) *PaperMatcherWorker {
	if interval <= 0 {
		interval = time.Second
	}
	return &PaperMatcherWorker{Matcher: m, Interval: interval, wake: make(chan struct{}, 1)}
}

// SubscribeState 订阅撮合工人就绪状态变化，用于模拟盘会话事件唤醒。
func (w *PaperMatcherWorker) SubscribeState() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	if w == nil {
		return ch, func() {}
	}
	w.subsMu.Lock()
	if w.subs == nil {
		w.subs = make(map[chan struct{}]struct{})
	}
	w.subs[ch] = struct{}{}
	w.subsMu.Unlock()
	return ch, func() {
		w.subsMu.Lock()
		delete(w.subs, ch)
		w.subsMu.Unlock()
	}
}

func (w *PaperMatcherWorker) notifyState() {
	if w == nil {
		return
	}
	w.subsMu.Lock()
	for ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	w.subsMu.Unlock()
}

func (w *PaperMatcherWorker) setReady(ready bool) {
	if w.ready.Swap(ready) != ready {
		w.notifyState()
	}
}

func (w *PaperMatcherWorker) setState(ready bool, lastError string) {
	prevReady := w.ready.Swap(ready)
	prevErr, _ := w.lastError.Swap(lastError).(string)
	if prevReady != ready || prevErr != lastError {
		w.notifyState()
	}
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
	w.setReady(false)
	defer w.setReady(false)
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
		w.setState(false, err.Error())
		return
	}
	if err := ctx.Err(); err != nil {
		w.setState(false, err.Error())
		return
	}
	w.setState(true, "")
}
