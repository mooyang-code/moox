package eventconsumer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mooyang-code/moox/packages/jetstream"
)

type subjectDeliveryHandler func(context.Context, *jetstream.Delivery, *deliveryHeartbeat) error

type subjectDispatcherMetricsHooks struct {
	newHeartbeat func(context.Context, *jetstream.Delivery) *deliveryHeartbeat
	onSubmit     func(*jetstream.Delivery)
	onStart      func(*jetstream.Delivery)
	onFinish     func(*jetstream.Delivery)
}

type subjectQueue struct {
	subject    string
	deliveries []*queuedDelivery
	running    bool
}

type queuedDelivery struct {
	delivery  *jetstream.Delivery
	heartbeat *deliveryHeartbeat
}

// subjectDispatcher is a scheduler rather than a plain worker pool: a
// subject queue is scheduled at most once, so one subject cannot have two
// active handlers.
type subjectDispatcher struct {
	ctx        context.Context
	cancel     context.CancelFunc
	maxWorkers int
	handler    subjectDeliveryHandler
	reporter   jetstream.ErrorReporter
	hooks      subjectDispatcherMetricsHooks
	ready      chan *subjectQueue
	queues     map[string]*subjectQueue
	mu         sync.Mutex
	closed     bool
	wg         sync.WaitGroup
}

func newSubjectDispatcher(parent context.Context, maxWorkers int, handler subjectDeliveryHandler, reporter jetstream.ErrorReporter, hooks ...subjectDispatcherMetricsHooks) *subjectDispatcher {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	ctx, cancel := context.WithCancel(parent)
	var metricsHooks subjectDispatcherMetricsHooks
	if len(hooks) > 0 {
		metricsHooks = hooks[0]
	}
	d := &subjectDispatcher{
		ctx: ctx, cancel: cancel, maxWorkers: maxWorkers, handler: handler, reporter: reporter,
		hooks: metricsHooks,
		ready: make(chan *subjectQueue, maxWorkers), queues: make(map[string]*subjectQueue),
	}
	d.wg.Add(maxWorkers)
	for i := 0; i < maxWorkers; i++ {
		go d.worker()
	}
	return d
}

func (d *subjectDispatcher) Dispatch(delivery *jetstream.Delivery) error {
	if d == nil || delivery == nil {
		return errors.New("storage view subject delivery is nil")
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("storage view subject dispatcher is closed")
	}
	queue := d.queues[delivery.Subject]
	if queue == nil {
		queue = &subjectQueue{subject: delivery.Subject}
		d.queues[delivery.Subject] = queue
	}
	var heartbeat *deliveryHeartbeat
	if d.hooks.newHeartbeat != nil {
		heartbeat = d.hooks.newHeartbeat(d.ctx, delivery)
	}
	queue.deliveries = append(queue.deliveries, &queuedDelivery{delivery: delivery, heartbeat: heartbeat})
	start := !queue.running
	if start {
		queue.running = true
	}
	d.mu.Unlock()
	if d.hooks.onSubmit != nil {
		d.hooks.onSubmit(delivery)
	}
	if start {
		select {
		case d.ready <- queue:
		case <-d.ctx.Done():
			return d.ctx.Err()
		}
	}
	return nil
}

func (d *subjectDispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case queue := <-d.ready:
			if queue == nil {
				continue
			}
			item, ok := d.next(queue)
			if !ok {
				continue
			}
			delivery := item.delivery
			if d.hooks.onStart != nil {
				d.hooks.onStart(delivery)
			}
			if d.handler != nil {
				if err := d.handler(d.ctx, delivery, item.heartbeat); err != nil && d.ctx.Err() == nil && d.reporter != nil {
					d.reporter.Report(fmt.Errorf("storage view subject %q delivery failed: %w", queue.subject, err))
				}
			}
			if item.heartbeat != nil {
				item.heartbeat.stop()
			}
			if d.hooks.onFinish != nil {
				d.hooks.onFinish(delivery)
			}
			d.finish(queue)
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *subjectDispatcher) next(queue *subjectQueue) (*queuedDelivery, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(queue.deliveries) == 0 {
		queue.running = false
		delete(d.queues, queue.subject)
		return nil, false
	}
	delivery := queue.deliveries[0]
	queue.deliveries = queue.deliveries[1:]
	return delivery, true
}

func (d *subjectDispatcher) finish(queue *subjectQueue) {
	d.mu.Lock()
	if len(queue.deliveries) == 0 {
		queue.running = false
		delete(d.queues, queue.subject)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	select {
	case d.ready <- queue:
	default:
		// Never let all workers block trying to requeue while ready already
		// contains other queues. The helper exits with the dispatcher.
		go d.enqueue(queue)
	}
}

func (d *subjectDispatcher) enqueue(queue *subjectQueue) {
	select {
	case d.ready <- queue:
	case <-d.ctx.Done():
	}
}

func (d *subjectDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	var queued []*deliveryHeartbeat
	if !d.closed {
		d.closed = true
		d.cancel()
		for _, queue := range d.queues {
			for _, item := range queue.deliveries {
				if item != nil && item.heartbeat != nil {
					queued = append(queued, item.heartbeat)
				}
			}
			queue.deliveries = nil
		}
	}
	d.mu.Unlock()
	for _, heartbeat := range queued {
		heartbeat.stop()
	}
	d.wg.Wait()
}
