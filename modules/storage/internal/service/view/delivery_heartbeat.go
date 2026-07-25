package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type deliveryHeartbeat struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	mu       sync.Mutex
	errs     []error
	metrics  *observability.ViewMetrics
}

func newDeliveryHeartbeat(ctx context.Context, delivery *jetstream.Delivery, interval time.Duration, metrics ...*observability.ViewMetrics) *deliveryHeartbeat {
	var metricSink *observability.ViewMetrics
	if len(metrics) > 0 {
		metricSink = metrics[0]
	}
	h := &deliveryHeartbeat{stopCh: make(chan struct{}), doneCh: make(chan struct{}), metrics: metricSink}
	if ctx == nil || delivery == nil || interval <= 0 {
		close(h.doneCh)
		return h
	}
	go func() {
		defer close(h.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				if err := delivery.InProgress(ctx); err != nil {
					log.Printf("storage view delivery in-progress failed: %v", err)
					if h.metrics != nil {
						h.metrics.IncInProgressError()
						h.metrics.ObserveDelivery("in_progress", "error")
					}
					h.report(fmt.Errorf("storage view delivery in-progress: %w", err))
				} else {
					if h.metrics != nil {
						h.metrics.ObserveDelivery("in_progress", "success")
					}
				}
			case <-h.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return h
}

func deliveryHeartbeatInterval(ackWait time.Duration) time.Duration {
	if ackWait <= 0 {
		ackWait = 120 * time.Second
	}
	interval := ackWait / 3
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < time.Second {
		interval = time.Second
	}
	return interval
}

func (h *deliveryHeartbeat) report(err error) {
	if h == nil || err == nil {
		return
	}
	h.mu.Lock()
	h.errs = append(h.errs, err)
	h.mu.Unlock()
}

func (h *deliveryHeartbeat) err() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return errors.Join(h.errs...)
}

func (h *deliveryHeartbeat) stop() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() { close(h.stopCh) })
	<-h.doneCh
}

// liveLeaseGate gives backfill writer priority. Once a writer is waiting,
// new real-time reads stop entering and the writer waits for readers to drain.
